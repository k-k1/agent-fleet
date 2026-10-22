package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// captureMCPLog returns what the standard logger writes until the test ends.
func captureMCPLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func (e *mcpOAuthEnv) mcpPost(token, body string, hdr map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	for k, v := range hdr {
		h[k] = v
	}
	return e.do(http.MethodPost, "/mcp", body, h)
}

func (e *mcpOAuthEnv) grantCalls(id string) []mcpsrv.CallRecord {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/mcp-oauth/grants/"+id+"/calls", "", nil)
	var out mcpCallsOut
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		e.t.Fatalf("calls: %d %s", rec.Code, rec.Body)
	}
	return out.Calls
}

func (e *mcpOAuthEnv) onlyGrantID() string {
	e.t.Helper()
	gs, err := e.mgr.store.ListMCPOAuthGrantsByIdentity(context.Background(), e.ident.ID, e.now().Unix())
	if err != nil || len(gs) != 1 {
		e.t.Fatalf("grants: %v %v", gs, err)
	}
	return gs[0].ID
}

const statelessMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientInfo":{"name":"probe","version":"0.1"},` +
	`"io.modelcontextprotocol/clientCapabilities":{}}`

func TestMCPCallsRecordedPerRequest(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	logs := captureMCPLog(t)
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	gid := e.onlyGrantID()

	// A stateless request whose Mcp-Method header is missing: validateStateless answers 400.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + statelessMeta + `}}`
	rec := e.mcpPost(tk.Access, body, map[string]string{
		"MCP-Protocol-Version": "2026-07-28", "Accept": "application/json, text/event-stream", "User-Agent": "probe-agent/1"})
	if rec.Code != 400 {
		t.Fatalf("stateless without Mcp-Method: %d %s", rec.Code, rec.Body)
	}
	// A legacy tools/list, answered with the tool set of a read-scoped grant.
	rec = e.mcpCall(tk.Access, "tools/list")
	var list struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Result.Tools) == 0 {
		t.Fatalf("tools/list: %d %s", rec.Code, rec.Body)
	}
	// A batch of a notification and a ping, and a notification on its own (202).
	e.mcpPost(tk.Access, `[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`, nil)
	if rec = e.mcpPost(tk.Access, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); rec.Code != 202 {
		t.Fatalf("notification: %d", rec.Code)
	}

	calls := e.grantCalls(gid)
	// newest first: notification, batch, tools/list, 400, then the connect-time probe (none)
	if len(calls) != 4 {
		t.Fatalf("want 4 calls, got %d: %+v", len(calls), calls)
	}
	note, batch, tl, bad := calls[0], calls[1], calls[2], calls[3]
	if note.Status != 202 || len(note.Items) != 1 || !note.Items[0].Notification {
		t.Errorf("notification: %+v", note)
	}
	if len(batch.Items) != 2 || batch.Items[0].Method != "notifications/initialized" || batch.Items[1].Method != "ping" || batch.Status != 200 {
		t.Errorf("batch: %+v", batch)
	}
	if len(tl.Items) != 1 || tl.Items[0].Tools == nil || *tl.Items[0].Tools != len(list.Result.Tools) || tl.Items[0].Era != "legacy" {
		t.Errorf("tools/list: %+v", tl)
	}
	if bad.Status != 400 || bad.HTTPMethod != "POST" || len(bad.Items) != 1 {
		t.Fatalf("400: %+v", bad)
	}
	it := bad.Items[0]
	if it.Era != "stateless" || it.MetaVersion != "2026-07-28" || it.ErrorCode != -32020 || !strings.Contains(it.ErrorMessage, "Mcp-Method") ||
		it.ClientName != "probe" || it.ClientVersion != "0.1" || it.Method != "tools/list" || it.Status != 400 {
		t.Errorf("400 item: %+v", it)
	}
	if bad.HeaderVersion != "2026-07-28" || bad.McpMethodHeader || bad.McpNameHeader || bad.Accept != "application/json, text/event-stream" ||
		bad.ContentType != "application/json" || bad.UserAgent != "probe-agent/1" || bad.Principal != "grant:"+gid || bad.ClientID != client {
		t.Errorf("400 request fields: %+v", bad)
	}

	line := lineWith(logs.String(), `status=400`)
	for _, want := range []string{`principal="grant:` + gid + `"`, `client_id="` + client + `"`, `mcp_method_header=false`, `user_agent="probe-agent/1"`, `"error_code":-32020`, `"era":"stateless"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %s: %s", want, line)
		}
	}
}

func lineWith(all, sub string) string {
	for _, l := range strings.Split(all, "\n") {
		if strings.Contains(l, "mcp-call") && strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestMCPCallsMethodNotAllowedAndAuthFailureAreLogOnly(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	logs := captureMCPLog(t)
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	gid := e.onlyGrantID()

	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		if rec := e.do(m, "/mcp", "", map[string]string{"Authorization": "Bearer " + tk.Access, "Accept": "text/event-stream"}); rec.Code != 405 {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
	if l := lineWith(logs.String(), `http_method="GET"`); !strings.Contains(l, `status=405`) || !strings.Contains(l, `principal="unauthenticated"`) ||
		!strings.Contains(l, `reason="method not allowed"`) || !strings.Contains(l, `accept="text/event-stream"`) {
		t.Errorf("405 GET line: %q", l)
	}
	if !strings.Contains(logs.String(), `http_method="DELETE" status=405`) {
		t.Errorf("DELETE not logged: %s", logs)
	}

	if rec := e.mcpCall("", "tools/list"); rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := e.mcpCall("afmcp_at_bogus", "tools/list"); rec.Code != 401 {
		t.Fatalf("bad token: %d", rec.Code)
	}
	if l := lineWith(logs.String(), `status=401`); !strings.Contains(l, `principal="unauthenticated"`) || !strings.Contains(l, `reason="no bearer token presented"`) {
		t.Errorf("401 line: %q", l)
	}
	if !strings.Contains(logs.String(), `reason="oauth token unknown, expired or its grant ended"`) {
		t.Errorf("bad token reason missing: %s", logs)
	}
	if n := len(e.grantCalls(gid)); n != 0 {
		t.Errorf("pre-auth and refused requests must not reach the buffer, got %d", n)
	}
}

func TestMCPCallsNeverRecordSecrets(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	logs := captureMCPLog(t)
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "write")
	gid := e.onlyGrantID()

	secretArg := "ARGUMENT-SECRET-7"
	e.mcpPost(tk.Access, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{"note":"`+secretArg+`"}}}`, nil)
	e.mcpCall(tk.Access, "tools/list")
	e.mcpCall(tk.Access+"x", "tools/list") // a refused token must not be echoed either

	buf := e.do(http.MethodGet, "/api/mcp-oauth/grants/"+gid+"/calls", "", nil).Body.String()
	if !strings.Contains(buf, "no_such_tool") {
		t.Fatalf("tool name expected in the buffer: %s", buf)
	}
	for _, s := range []string{tk.Access, devIdPHash(tk.Access), hashPAT(tk.Access), tk.Refresh, devIdPHash(tk.Refresh), "Bearer", secretArg} {
		if strings.Contains(buf, s) || strings.Contains(logs.String(), s) {
			t.Errorf("secret %q found in the buffer or the log", s)
		}
	}
}

func TestMCPCallsOwnerOnly(t *testing.T) {
	e := newMCPOAuthEnv(t)
	registerPATRoutes(e.mux, config{mgr: e.mgr})
	captureMCPLog(t)
	client := e.registerClient(loopbackCB)
	tk := e.connect(client, loopbackCB, "read")
	gid := e.onlyGrantID()
	e.mcpCall(tk.Access, "tools/list")

	ctx := context.Background()
	other, err := e.mgr.store.UpsertIdentity(ctx, "", "other-user", "")
	if err != nil {
		t.Fatal(err)
	}
	patTok, patHash := newPATToken()
	if err := e.mgr.store.CreatePAT(ctx, store.PAT{ID: "pat-owned", IdentityID: e.ident.ID, MembershipID: e.membershipID(), Scope: "read", Name: "t", CreatedAt: store.NowTS()}, patHash); err != nil {
		t.Fatal(err)
	}
	e.mcpCall(patTok, "tools/list")

	apps, pats := mcpConnectedAPI{memberAuth{e.mgr}}, newPATAPI(e.mgr)
	get := func(h func(http.ResponseWriter, *http.Request, store.Identity), id string, who store.Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/calls", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h(w, r, who)
		return w
	}
	for name, c := range map[string]struct {
		h  func(http.ResponseWriter, *http.Request, store.Identity)
		id string
	}{"grant": {apps.calls, gid}, "pat": {pats.calls, "pat-owned"}} {
		own := get(c.h, c.id, e.ident)
		if own.Code != 200 || !strings.Contains(own.Body.String(), `"tools/list"`) {
			t.Errorf("%s owner: %d %s", name, own.Code, own.Body)
		}
		foreign, unknown := get(c.h, c.id, other), get(c.h, "unknown", other)
		if foreign.Code != 404 || foreign.Code != unknown.Code || foreign.Body.String() != unknown.Body.String() {
			t.Errorf("%s IDOR: %d %s / %d %s", name, foreign.Code, foreign.Body, unknown.Code, unknown.Body)
		}
	}
	if got := e.mgr.mcpCalls.Recent(mcpsrv.CallKey(false, "pat-owned")); len(got) != 1 || got[0].Principal != "pat:pat-owned" {
		t.Errorf("PAT ring: %+v", got)
	}
}
