package mcpreg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func stdioDef(name string) ServerDef {
	return ServerDef{Name: name, Origin: OriginUser, Transport: TransportStdio, Command: "/bin/true"}
}

func httpDef(name, url string) ServerDef {
	return ServerDef{Name: name, Origin: OriginUser, Transport: TransportHTTP, URL: url}
}

func TestValidateAccepts(t *testing.T) {
	for _, d := range []ServerDef{
		stdioDef("wiki"),
		httpDef("tickets", "https://mcp.example.com/mcp"),
		{Name: "a", Origin: OriginUser, Transport: TransportStdio, Command: "npx",
			Args: []string{"-y", "srv"}, Env: map[string]string{"API_KEY": "x"},
			Kinds: []string{session.KindClaude, session.KindCodex}, TimeoutMS: 5000},
	} {
		if err := Validate(d); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", d.Name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]ServerDef{
		"empty name":                {Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"name with a symbol":        {Name: "my server", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"leading hyphen":            {Name: "-srv", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"reserved name":             {Name: "af", Origin: OriginUser, Transport: TransportStdio, Command: "x"},
		"stdio without a command":   {Name: "s", Origin: OriginUser, Transport: TransportStdio},
		"stdio with a URL":          {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", URL: "https://e.com"},
		"http without a URL":        {Name: "s", Origin: OriginUser, Transport: TransportHTTP},
		"http with a command":       {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Command: "x"},
		"non-http scheme":           {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "ftp://e.com"},
		"credentials in the URL":    {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://u:p@e.com"},
		"unsupported transport":     {Name: "s", Origin: OriginUser, Transport: "sse", URL: "https://e.com"},
		"invalid env var name":      {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", Env: map[string]string{"bad-name": "v"}},
		"colon in a header name":    {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Headers: map[string]string{"A:B": "v"}},
		"newline in a header value": {Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com", Headers: map[string]string{"A": "v\r\nX: y"}},
		"unknown kind":              {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", Kinds: []string{"shell"}},
		"timeout out of range":      {Name: "s", Origin: OriginUser, Transport: TransportStdio, Command: "x", TimeoutMS: 10},
		"tenant-distributed stdio":  {Name: "s", Origin: OriginTenant, Transport: TransportStdio, Command: "x"},
	}
	for label, d := range cases {
		err := Validate(d)
		if err == nil {
			t.Errorf("%s: Validate = nil, want error", label)
			continue
		}
		var ve *ValidationError
		if !asValidation(err, &ve) {
			t.Errorf("%s: error is %T, want *ValidationError (handlers map it to 400)", label, err)
		}
	}
}

func asValidation(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

// Rejecting tenant-distributed stdio is the core of ADR0031 decision 2. It only works paired
// with the design that never grows a column for it, so pin down that there is no "it passes
// as long as some other field is filled in" loophole.
func TestTenantStdioAlwaysRejected(t *testing.T) {
	d := ServerDef{Name: "s", Origin: OriginTenant, Transport: TransportStdio, Command: "/bin/sh", Args: []string{"-c", "curl evil"}}
	if err := Validate(d); err == nil {
		t.Fatal("tenant-distributed stdio was accepted")
	}
}

func TestMaskAndMergeSecrets(t *testing.T) {
	stored := ServerDef{
		Name: "s", Origin: OriginUser, Transport: TransportHTTP, URL: "https://e.com",
		Headers: map[string]string{"Authorization": "Bearer real", "X-Team": "ops"},
	}
	masked := Masked(stored)
	for k, v := range masked.Headers {
		if v != MaskedValue {
			t.Fatalf("Masked leaked %s=%q", k, v)
		}
	}
	if len(masked.Headers) != 2 {
		t.Fatalf("masking dropped the header names too: %v", masked.Headers)
	}

	// A value returned still masked keeps the stored value, a real value overwrites it, and a
	// missing key deletes it.
	incoming := masked
	incoming.Headers = map[string]string{"Authorization": MaskedValue, "X-Team": "sre"}
	got := MergeSecrets(incoming, stored)
	if got.Headers["Authorization"] != "Bearer real" {
		t.Fatalf("the secret was lost on a mask round trip: %q", got.Headers["Authorization"])
	}
	if got.Headers["X-Team"] != "sre" {
		t.Fatalf("the new value was not applied: %q", got.Headers["X-Team"])
	}

	// A mask with no stored value behind it is dropped, so that "***" is never sent as a real
	// credential.
	got = MergeSecrets(ServerDef{Headers: map[string]string{"New": MaskedValue}}, stored)
	if _, ok := got.Headers["New"]; ok {
		t.Fatalf(`an unbacked mask value was stored: %v`, got.Headers)
	}
}

func TestReadyHoldsBackUnfilledSecrets(t *testing.T) {
	// A definition the tenant distributed with user_secret, carrying only header names, must
	// not materialize until the values are filled in.
	d := httpDef("t", "https://e.com")
	d.Enabled = true
	d.Headers = map[string]string{"Authorization": ""}
	if Ready(d) {
		t.Fatal("a definition with an empty header value is ready")
	}
	d.Headers["Authorization"] = MaskedValue
	if Ready(d) {
		t.Fatal("a definition still holding a mask value is ready")
	}
	d.Headers["Authorization"] = "Bearer x"
	if !Ready(d) {
		t.Fatal("a definition with all values filled in is not ready")
	}
	if Ready(ServerDef{Enabled: false, Transport: TransportStdio, Command: "x"}) {
		t.Fatal("a disabled definition is ready")
	}
}

func TestAppliesToKinds(t *testing.T) {
	d := stdioDef("s")
	d.Enabled, d.Targets = true, Targets{Session: true}
	if !AppliesTo(d, session.KindClaude) {
		t.Fatal("an unset kinds list should apply to every kind")
	}
	d.Kinds = []string{session.KindCodex}
	if AppliesTo(d, session.KindClaude) || !AppliesTo(d, session.KindCodex) {
		t.Fatal("the kinds filter has no effect")
	}
	d.Targets = Targets{Assistant: true}
	if AppliesTo(d, session.KindCodex) {
		t.Fatal("a definition that does not target sessions is about to be handed to one")
	}
}

func TestComposePrecedence(t *testing.T) {
	s := &secrets.Data{
		PagerDuty: &secrets.PagerDutyCreds{APIKey: "k"},
		MCP: []ServerDef{
			{ID: "u1", Name: "wiki", Origin: OriginUser, Enabled: true},
			{ID: "u2", Name: "Tickets", Origin: OriginUser, Enabled: true}, // collides with the tenant's (case-insensitively)
		},
	}
	tc := tenantCache{FetchedAt: 42, Servers: []ServerDef{{ID: "t1", Name: "tickets", Enabled: true}}}

	reg := compose(s, tc, map[string]bool{})
	names := map[string]ServerDef{}
	for _, d := range reg.Servers {
		names[d.Name] = d
	}
	if _, ok := names["pagerduty"]; !ok {
		t.Fatal("a connected builtin integration is missing from the list")
	}
	if names["tickets"].Origin != OriginTenant || names["tickets"].ID != "t1" {
		t.Fatalf("a name collision did not resolve in the tenant's favour: %+v", names["tickets"])
	}
	if _, ok := names["Tickets"]; ok {
		t.Fatal("the colliding user definition is still enabled")
	}
	if len(reg.Shadowed) != 1 || reg.Shadowed[0] != "Tickets" {
		t.Fatalf("shadowed = %v, want [Tickets]", reg.Shadowed)
	}
	if reg.TenantFetchedAt != 42 {
		t.Fatalf("TenantFetchedAt = %d, want 42", reg.TenantFetchedAt)
	}
	// Sorted by name, so the Console's list does not reshuffle on every call.
	for i := 1; i < len(reg.Servers); i++ {
		if strings.ToLower(reg.Servers[i-1].Name) > strings.ToLower(reg.Servers[i].Name) {
			t.Fatalf("not sorted by name: %v", reg.Servers)
		}
	}
}

// dropAF removes the af builtin (docs/log/51 Phase 3 §self-report fast path) from a registry
// slice. af holds no connection details and is always present, so it is excluded from
// assertions that check "only what was registered shows up" — otherwise tests that have
// nothing to do with af start counting whether af is there.
func dropAF(defs []ServerDef) []ServerDef {
	var out []ServerDef
	for _, d := range defs {
		if d.ID != BuiltinAF {
			out = append(out, d)
		}
	}
	return out
}

func TestComposeBuiltinNeedsConnection(t *testing.T) {
	reg := compose(&secrets.Data{}, tenantCache{}, map[string]bool{})
	for _, d := range dropAF(reg.Servers) {
		if d.Origin == OriginBuiltin {
			t.Fatalf("an unconnected builtin integration is listed: %s", d.Name)
		}
	}
}

// TestComposeAlwaysHasSelfReport: af (the session-side server for self-reporting plus
// Chromium Attach View) needs no connection, is always present, and is handed to sessions
// only. If this ever leaned towards assistant, an operator conversation could file a
// completion report to itself.
func TestComposeAlwaysHasSelfReport(t *testing.T) {
	reg := compose(&secrets.Data{}, tenantCache{}, map[string]bool{})
	var af *ServerDef
	for i := range reg.Servers {
		if reg.Servers[i].ID == BuiltinAF {
			af = &reg.Servers[i]
		}
	}
	if af == nil {
		t.Fatal("the af builtin is missing")
	}
	if !af.Targets.Session || af.Targets.Assistant {
		t.Fatalf("af targets = %+v, want session only", af.Targets)
	}
	if !Ready(*af) || af.Origin != OriginBuiltin {
		t.Fatalf("af = %+v", *af)
	}
	if got, want := af.Args, []string{"mcp-stdio", "--self-report", "--chromium-attach"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("af args = %v, want %v", got, want)
	}
}

// TestComposeAWSBuiltin: AWS MCP (Agent Toolkit for AWS — docs/log/25 §AWS MCP) appears only
// once connected and is handed to both the assistant and sessions. Showing up in sessions too,
// unlike the other ops integrations, is the whole point: if this leaned towards assistant only,
// it would silently regress to "connected, yet no AWS tools are visible from a session".
func TestComposeAWSBuiltin(t *testing.T) {
	reg := compose(&secrets.Data{AWS: &secrets.AWSConn{
		AWSProfileRef: secrets.AWSProfileRef{Profile: "ops"},
	}}, tenantCache{}, map[string]bool{})
	var aws *ServerDef
	for i := range reg.Servers {
		if reg.Servers[i].ID == BuiltinAWS {
			aws = &reg.Servers[i]
		}
	}
	if aws == nil {
		t.Fatal("the aws builtin is missing")
	}
	if !aws.Targets.Session || !aws.Targets.Assistant {
		t.Fatalf("aws targets = %+v, want session+assistant", aws.Targets)
	}
	if got, want := aws.Args, []string{"mcp-run", "aws"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aws args = %v, want %v", got, want)
	}
	// No profile means no credentials to reach for, so the connection is not ready (nothing
	// can be signed).
	if BuiltinReady(BuiltinAWS, &secrets.Data{AWS: &secrets.AWSConn{}}) {
		t.Fatal("an aws connection with an empty profile is ready")
	}
}

func TestComposeTenantOptOut(t *testing.T) {
	tc := tenantCache{Servers: []ServerDef{{ID: "t1", Name: "tickets", Enabled: true}}}
	reg := compose(&secrets.Data{}, tc, map[string]bool{"t1": true})
	servers := dropAF(reg.Servers)
	if len(servers) != 1 || servers[0].Enabled {
		t.Fatalf("the local opt-out has no effect: %+v", servers)
	}
}

// --- store CRUD (isolated HOME) ---------------------------------------------

func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
}

func TestCRUDRoundTrip(t *testing.T) {
	withTempHome(t)

	created, err := Create(ServerDef{
		Name: "wiki", Transport: TransportHTTP, URL: "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer secret"},
		Enabled: true, Targets: Targets{Session: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || created.Origin != OriginUser || created.CreatedAt == 0 {
		t.Fatalf("Create did not fill in id/origin/createdAt: %+v", created)
	}

	if _, err := Create(ServerDef{Name: "WIKI", Transport: TransportStdio, Command: "x"}); err == nil {
		t.Fatal("the same name differing only in case was accepted")
	}

	// The secret must survive a mask round trip (the Console always sends the mask back).
	edit := Masked(created)
	edit.Label = "社内 Wiki"
	updated, err := Update(created.ID, edit)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("the secret was lost on update: %q", updated.Headers["Authorization"])
	}
	if updated.Label != "社内 Wiki" || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("wrong update result: %+v", updated)
	}

	if err := SetEnabled(created.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, err := Get(created.ID)
	if err != nil || got.Enabled {
		t.Fatalf("Get after disable = %+v, %v", got, err)
	}

	if err := Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(created.ID); err == nil {
		t.Fatal("still retrievable after Delete")
	}
}

func TestBuiltinIsReadOnly(t *testing.T) {
	withTempHome(t)
	s, _ := secrets.Load()
	s.PagerDuty = &secrets.PagerDutyCreds{APIKey: "k"}
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Update(BuiltinPagerDuty, stdioDef("pagerduty")); err != ErrReadOnly {
		t.Fatalf("Update err = %v, want ErrReadOnly", err)
	}
	if err := Delete(BuiltinPagerDuty); err != ErrReadOnly {
		t.Fatalf("Delete err = %v, want ErrReadOnly", err)
	}
}

func TestForSessionFiltersAndForAssistant(t *testing.T) {
	withTempHome(t)
	if _, err := Create(ServerDef{
		Name: "sess", Transport: TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: Targets{Session: true}, Kinds: []string{session.KindCodex},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Create(ServerDef{
		Name: "chat", Transport: TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: Targets{Assistant: true},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := ForSession(session.KindCodex)
	if got = dropAF(got); err != nil || len(got) != 1 || got[0].Name != "sess" {
		t.Fatalf("ForSession(codex) = %+v, %v", got, err)
	}
	if got, _ := ForSession(session.KindClaude); len(dropAF(got)) != 0 {
		t.Fatalf("ForSession(claude) = %+v, want empty", dropAF(got))
	}
	forChat, err := ForAssistant(session.KindClaude)
	if err != nil || len(forChat) != 1 {
		t.Fatalf("ForAssistant = %+v, %v", forChat, err)
	}
}

// --- probe ------------------------------------------------------------------

// TestHelperMCPServer is the fake MCP server used by the probe tests. It re-executes the test
// binary to play the part of a stdio server (the standard trick for not dragging in an
// external dependency). AF_MCP_TEST_HELPER selects the era:
//
//	stateless — 2026-07-28. Answers server/discover
//	legacy    — 2025-*. Returns -32601 to server/discover and waits for initialize
//	silent    — a badly behaved old server. Ignores unknown methods (the timeout path of era detection)
func TestHelperMCPServer(t *testing.T) {
	mode := os.Getenv("AF_MCP_TEST_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	// Some real servers print a one-line banner before speaking the protocol, so this doubles
	// as a check that we tolerate one.
	fmt.Println("fake mcp server starting")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "server/discover":
			if mode != "stateless" {
				if mode == "legacy" {
					emitErr(m.ID, -32601, "method not found")
				}
				continue // silent: ignore it
			}
			// The stateless era requires _meta on every request. Check here that the probe
			// really attaches it — otherwise a missing _meta only surfaces against a real
			// server.
			if !hasStatelessMeta(m.Params) {
				emitErr(m.ID, -32602, "missing _meta")
				continue
			}
			emit(m.ID, map[string]any{
				"resultType":        "complete",
				"supportedVersions": []string{"2026-07-28"},
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"serverInfo":        map[string]any{"name": "fake", "version": "9.9"},
			})
		case "initialize":
			if mode == "stateless" {
				emitErr(m.ID, -32601, "method not found")
				continue
			}
			emit(m.ID, map[string]any{"serverInfo": map[string]any{"name": "legacy-fake", "version": "1.1"}})
		case "tools/list":
			if mode == "stateless" && !hasStatelessMeta(m.Params) {
				emitErr(m.ID, -32602, "missing _meta")
				continue
			}
			emit(m.ID, map[string]any{"tools": []map[string]any{{"name": "search"}, {"name": "fetch"}}})
		}
	}
	os.Exit(0)
}

func hasStatelessMeta(params json.RawMessage) bool {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	for _, k := range []string{metaProtocolVersion, metaClientInfo, metaClientCaps} {
		if _, ok := p.Meta[k]; !ok {
			return false
		}
	}
	return true
}

func emit(id any, result map[string]any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(b))
}

func emitErr(id any, code int, msg string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg}})
	fmt.Println(string(b))
}

func helperDef(mode string) ServerDef {
	d := stdioDef("fake")
	d.Command = os.Args[0]
	d.Args = []string{"-test.run=TestHelperMCPServer"}
	d.Env = map[string]string{"AF_MCP_TEST_HELPER": mode}
	return d
}

func TestProbeStdioStateless(t *testing.T) {
	res := Probe(context.Background(), helperDef("stateless"))
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.Revision != ProtocolVersion {
		t.Fatalf("revision = %q, want %q", res.Revision, ProtocolVersion)
	}
	if res.ServerName != "fake" || res.ServerVersion != "9.9" {
		t.Fatalf("serverInfo = %s %s", res.ServerName, res.ServerVersion)
	}
	if res.ToolCount != 2 || len(res.SupportedVersions) != 1 {
		t.Fatalf("tools=%d supported=%v", res.ToolCount, res.SupportedVersions)
	}
}

// An old server (-32601 to server/discover) falls back to the initialize handshake.
func TestProbeStdioFallsBackToLegacy(t *testing.T) {
	res := Probe(context.Background(), helperDef("legacy"))
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.Revision != ProtocolVersionLegacy {
		t.Fatalf("revision = %q, want %q", res.Revision, ProtocolVersionLegacy)
	}
	if res.ServerName != "legacy-fake" || res.ToolCount != 2 {
		t.Fatalf("unexpected: %+v", res)
	}
}

// Even against a badly behaved old server that ignores unknown methods, the short deadline of
// era detection falls back to the legacy era and succeeds, instead of hanging until the
// probe's overall timeout.
func TestProbeStdioFallsBackWhenDiscoverIgnored(t *testing.T) {
	d := helperDef("silent")
	d.TimeoutMS = 20000
	start := time.Now()
	res := Probe(context.Background(), d)
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.Revision != ProtocolVersionLegacy {
		t.Fatalf("revision = %q, want %q", res.Revision, ProtocolVersionLegacy)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("era detection is hanging: %v", el)
	}
}

func TestProbeStdioReportsBrokenCommand(t *testing.T) {
	d := stdioDef("broken")
	d.Command = "/nonexistent/mcp-server"
	res := Probe(context.Background(), d)
	if res.OK || res.Error == "" {
		t.Fatalf("a nonexistent command was treated as success: %+v", res)
	}
}

// The stateless era over HTTP. Pins down that the probe attaches the required headers
// (MCP-Protocol-Version / Mcp-Method) and _meta correctly, and that it does not send
// Mcp-Session-Id, which this era dropped.
func TestProbeHTTPStateless(t *testing.T) {
	var sawAuth, sawVerHdr, sawSession string
	var sawMethods []string
	var metaOK = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &m)
		sawAuth = r.Header.Get("Authorization")
		sawVerHdr = r.Header.Get("MCP-Protocol-Version")
		sawSession += r.Header.Get("Mcp-Session-Id")
		sawMethods = append(sawMethods, r.Header.Get("Mcp-Method"))
		if r.Header.Get("Mcp-Method") != m.Method || !hasStatelessMeta(m.Params) {
			metaOK = false
		}
		w.Header().Set("Content-Type", "application/json")
		switch m.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{
					"resultType":        "complete",
					"supportedVersions": []string{"2026-07-28"},
					"_meta":             map[string]any{metaServerInfo: map[string]any{"name": "remote", "version": "1.2"}},
				}})
		case "tools/list":
			// An SSE response must be accepted too (a server may return either form).
			w.Header().Set("Content-Type", "text/event-stream")
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"resultType": "complete", "tools": []map[string]any{{"name": "query"}}}})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		}
	}))
	defer srv.Close()

	d := httpDef("remote", srv.URL)
	d.Headers = map[string]string{"Authorization": "Bearer tok"}
	res := Probe(context.Background(), d)
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.Revision != ProtocolVersion {
		t.Fatalf("revision = %q, want %q", res.Revision, ProtocolVersion)
	}
	// A response whose serverInfo exists only under _meta — checks that both shapes are read.
	if res.ServerName != "remote" || res.ToolCount != 1 || res.Tools[0] != "query" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !metaOK {
		t.Fatal("a required header or _meta is missing from the request")
	}
	if sawAuth != "Bearer tok" {
		t.Fatalf("the registered header was not sent: %q", sawAuth)
	}
	if sawVerHdr != ProtocolVersion {
		t.Fatalf("MCP-Protocol-Version = %q", sawVerHdr)
	}
	if sawSession != "" {
		t.Fatalf("sending Mcp-Session-Id, which this era dropped: %q", sawSession)
	}
	if len(sawMethods) != 2 || sawMethods[0] != "server/discover" || sawMethods[1] != "tools/list" {
		t.Fatalf("unexpected order of Mcp-Method: %v", sawMethods)
	}
}

// An old HTTP server (-32601 to server/discover) falls back to initialize and carries the
// Mcp-Session-Id forward.
func TestProbeHTTPFallsBackToLegacy(t *testing.T) {
	var sawSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch m.Method {
		case "server/discover":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"serverInfo": map[string]any{"name": "old", "version": "0.1"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sawSession = r.Header.Get("Mcp-Session-Id")
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "old_tool"}}}})
		}
	}))
	defer srv.Close()

	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if !res.OK {
		t.Fatalf("Probe failed: %s / %s", res.Error, res.Detail)
	}
	if res.Revision != ProtocolVersionLegacy || res.ServerName != "old" || res.ToolCount != 1 {
		t.Fatalf("unexpected: %+v", res)
	}
	if sawSession != "sess-1" {
		t.Fatalf("Mcp-Session-Id was not carried forward in the legacy era: %q", sawSession)
	}
}

// When a modern server answers 400 with a known modern error, we must not fall back to the
// legacy era (spec: a modern error in the body means "this is a modern server, fix the
// request").
func TestProbeHTTPModernErrorDoesNotFallBack(t *testing.T) {
	var sawInitialize bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.Method == "initialize" {
			sawInitialize = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
			"error": map[string]any{"code": -32022, "message": "unsupported protocol version",
				"data": map[string]any{"supported": []string{"2026-07-28"}, "requested": "2026-07-28"}}})
	}))
	defer srv.Close()

	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.OK {
		t.Fatal("a modern error was treated as success")
	}
	if sawInitialize {
		t.Fatal("fell back to the legacy handshake against a modern server")
	}
}

func TestProbeHTTPReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	res := Probe(context.Background(), httpDef("remote", srv.URL))
	if res.OK {
		t.Fatal("a 401 was treated as success")
	}
	if !strings.Contains(res.Detail, "nope") {
		t.Fatalf("the response body is missing from detail: %q", res.Detail)
	}
}
