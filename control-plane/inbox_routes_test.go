package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const inboxTestEmailHeader = "X-Test-Email"

// inboxEnv is the real route table over a SQLite store, with proxy auth so each request can
// name its member. A, B and C are members of the default tenant T1. X is a member of T2
// only, and B is a member of both.
type inboxEnv struct {
	t       *testing.T
	st      *store.SQL
	mgr     *manager
	mux     *http.ServeMux
	t1, t2  store.Tenant
	members map[string]string // "a", "b", "c" (T1), "x", "b2" (T2) -> membership id
}

func newInboxEnv(t *testing.T) *inboxEnv {
	t.Helper()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return newInboxEnvOn(t, st)
}

// newInboxEnvOn is newInboxEnv on a store the caller opened, for the routes whose behaviour
// is a conditional update and so has to be shown on Postgres as well.
func newInboxEnvOn(t *testing.T, st *store.SQL) *inboxEnv {
	t.Helper()
	restoreAuthExemptions(t)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t1, _ := st.EnsureDefaultTenant(ctx)
	t2, err := st.CreateTenant(ctx, "t2", "T2")
	if err != nil {
		t.Fatal(err)
	}
	mgr := &manager{rts: map[string]cachedRT{}, store: st, dataRoot: t.TempDir(), authMode: "proxy",
		emailHeader: inboxTestEmailHeader, provisionMode: "auto", defaultTenantID: t1.ID, conns: newConnRegistry(),
		rtFactory: inboxStubFactory{}}
	e := &inboxEnv{t: t, st: st, mgr: mgr, t1: t1, t2: t2, members: map[string]string{}}
	for _, m := range []struct{ name, tenant string }{
		{"a", t1.ID}, {"b", t1.ID}, {"c", t1.ID}, {"x", t2.ID}, {"b2", t2.ID},
	} {
		email := strings.TrimSuffix(m.name, "2") + "@example.com"
		id, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
		if err != nil {
			t.Fatal(err)
		}
		mem, err := st.EnsureMembership(ctx, id.ID, m.tenant, "member")
		if err != nil {
			t.Fatal(err)
		}
		e.members[m.name] = mem.ID
	}
	e.mux = buildMux(config{consoleDir: t.TempDir(), mgr: mgr, egressDedup: &egressAuditDedup{}})
	return e
}

// inboxStubFactory lets a withResolved route build its runtime here, so the positive
// control below runs to the end instead of panicking after the record was created.
type inboxStubFactory struct{}

func (inboxStubFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	return stubRuntime{}
}

func key(name string) string { return sanitizeUser(name + "@example.com") }

// do sends one request as `who` ("" = nobody signed in). tenant goes into X-AF-Tenant.
func (e *inboxEnv) do(who, method, path string, body any, tenant ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rd)
	if who != "" {
		req.Header.Set(inboxTestEmailHeader, who+"@example.com")
	}
	if len(tenant) > 0 {
		req.Header.Set("X-AF-Tenant", tenant[0])
	} else if who == "b" {
		// b belongs to both tenants, so an unqualified request would be 409
		// tenant_selection_required and every refusal would look like a pass.
		req.Header.Set("X-AF-Tenant", e.t1.Slug)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

type itemResp struct {
	Item inboxItemWire `json:"item"`
}

type errResp struct {
	Error struct {
		Code       string          `json:"code"`
		Message    string          `json:"message"`
		State      string          `json:"state"`
		Assignee   inboxPersonWire `json:"assignee"`
		Recipients []string        `json:"recipients"`
	} `json:"error"`
}

func (e *inboxEnv) send(who, kind string, to ...string) string {
	e.t.Helper()
	var keys []string
	for _, n := range to {
		keys = append(keys, key(n))
	}
	rec := e.do(who, "POST", "/api/inbox/items", map[string]any{"kind": kind, "recipients": keys, "body": "please look at the flaky test"})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	return decode[itemResp](e.t, rec).Item.ID
}

func (e *inboxEnv) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// inboxRoutes is every registered route under /api/inbox, read from the live route table.
func inboxRoutes(t *testing.T, mux *http.ServeMux) []string {
	var out []string
	for _, line := range muxRoutes(t, mux) {
		if strings.Contains(line, " /api/inbox") {
			out = append(out, line)
		}
	}
	return out
}

// TestInboxRoutesNeverCreateWorkspace is docs/log/130 W-1/W-2 as a handler test: every inbox
// route, called by a member that has no workspace record, leaves `workspace` empty. The
// routes come from the live table, so a new route is exercised without anyone adding it
// here, and one registered through withResolved fails (the positive control below shows the
// check notices a withResolved route in this environment).
func TestInboxRoutesNeverCreateWorkspace(t *testing.T) {
	e := newInboxEnv(t)
	dm := e.send("a", "dm", "b")
	req := e.send("a", "request", "b", "c")
	routes := inboxRoutes(t, e.mux)
	if len(routes) < 17 {
		t.Fatalf("only %d inbox routes found: %v", len(routes), routes)
	}
	bodies := map[string]any{
		"POST /api/inbox/items":             map[string]any{"kind": "dm", "recipients": []string{key("a")}, "body": "hi"},
		"PUT /api/inbox/items/{id}/pr":      map[string]any{"url": "https://example.com/pr/1"},
		"POST /api/inbox/cursor":            map[string]any{"seq": 1},
		"PUT /api/inbox/settings":           map[string]any{"acceptFrom": "tenant", "dnd": false},
		"POST /api/inbox/items/{id}/launch": map[string]any{"dir": "", "kind": "claude"},
	}
	for _, line := range routes {
		method, pattern, _ := strings.Cut(line, " ")
		path := pattern
		if strings.Contains(path, "{id}") {
			id := req
			if strings.HasSuffix(path, "/read") {
				id = dm
			}
			path = strings.ReplaceAll(path, "{id}", id)
		}
		path = strings.ReplaceAll(path, "{userKey}", key("c"))
		if strings.Contains(path, "{") {
			t.Fatalf("%s: a path parameter this test does not know; teach it before adding the route", line)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		var rd io.Reader
		if b, ok := bodies[method+" "+pattern]; ok {
			raw, _ := json.Marshal(b)
			rd = strings.NewReader(string(raw))
		}
		r := httptest.NewRequest(method, path, rd).WithContext(ctx)
		r.Header.Set(inboxTestEmailHeader, "b@example.com")
		r.Header.Set("X-AF-Tenant", e.t1.Slug)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, r)
		cancel()
		if rec.Code >= 500 {
			t.Errorf("%s: %d %s", line, rec.Code, rec.Body.String())
		}
		if n := e.count("workspace"); n != 0 {
			t.Fatalf("%s (status %d) created %d workspace record(s)", line, rec.Code, n)
		}
	}
	// The claim above made b the assignee, so launch would now pass the claim check and
	// resolve the workspace. That is the one inbox action that is meant to.
}

// TestInboxRouteCheckPositiveControl shows the check above would see a withResolved route:
// in the same environment one such call creates the workspace record.
func TestInboxRouteCheckPositiveControl(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	_ = e.do("b", "GET", "/api/notifications", nil, e.t1.Slug)
	if n := e.count("workspace"); n == 0 {
		t.Fatal("a withResolved route created no workspace record here, so the inbox check proves nothing")
	}
}

// TestInboxRequiresSignIn is docs/log/130 A-1, and A-9's "an agent credential is not a
// person": without a signed-in member every inbox route is 401 and nothing is written.
func TestInboxRequiresSignIn(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b")
	before := e.count("inbox_item")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/inbox"}, {"POST", "/api/inbox/items"}, {"POST", "/api/inbox/items/" + id + "/claim"},
		{"POST", "/api/inbox/items/" + id + "/withdraw"}, {"GET", "/api/inbox/stream"},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"kind":"dm","recipients":["b-example-com"],"body":"x"}`))
		req.Header.Set("Authorization", "Bearer memo-token-or-agent-token")
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d, want 401", c.method, c.path, rec.Code)
		}
	}
	if e.count("inbox_item") != before {
		t.Fatal("an unauthenticated request wrote an item")
	}
}

// TestInboxHasNoAgentSurface is the code half of docs/log/130 A-9: no inbox route under
// /internal/, and no agent tool that sends or lists the roster (decision 11).
func TestInboxHasNoAgentSurface(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	for _, line := range muxRoutes(t, e.mux) {
		if strings.Contains(line, "/internal/") && strings.Contains(strings.ToLower(line), "inbox") {
			t.Errorf("an inbox route under /internal/: %s", line)
		}
	}
	scanned := 0
	for _, dir := range []string{"internal/mcpsrv", "../workspace/agent/internal/mcpx"} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, ent := range ents {
			if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			if strings.Contains(string(b), "/api/inbox") {
				t.Errorf("%s/%s reaches the inbox API: agents draft through add_memo only", dir, ent.Name())
			}
		}
	}
	if scanned < 5 {
		t.Fatalf("only %d tool source files scanned: the check has gone silent", scanned)
	}
}

// TestInboxInvalidRecipientsAreIndistinguishable is docs/log/130 A-2: an unknown key, a
// member of another tenant and a removed member are refused with the same status, code and
// message, and an item with one bad recipient is not created for the good ones either.
func TestInboxInvalidRecipientsAreIndistinguishable(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	if err := e.st.SetMembershipStatus(context.Background(), e.members["c"], "inactive"); err != nil {
		t.Fatal(err)
	}
	var first *httptest.ResponseRecorder
	for _, bad := range []string{"nobody-example-com", key("x"), key("c")} {
		rec := e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "request", "recipients": []string{key("b"), bad}, "body": "x"})
		if rec.Code != http.StatusBadRequest || decode[errResp](t, rec).Error.Code != errCodeInboxInvalidRecipient {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
		if first == nil {
			first = rec
		} else if rec.Body.String() != first.Body.String() {
			t.Fatalf("answers differ, which tells the sender who exists:\n%s\n%s", first.Body.String(), rec.Body.String())
		}
	}
	if n := e.count("inbox_item") + e.count("inbox_participant"); n != 0 {
		t.Fatalf("a refused send left %d row(s)", n)
	}
}

// TestInboxNeverReachesTheMemoQueue is the first half of docs/log/130 T-1: sending, reading
// and claiming put nothing into anybody's memo queue, which is what list_memos and a flush
// read. Take-in, the only way in, is the second half of P1-A.
func TestInboxNeverReachesTheMemoQueue(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b")
	e.send("a", "dm", "b")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/read", nil)
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	if e.count("inbox_item") != 2 {
		t.Fatal("the items were not created, so the check below proves nothing")
	}
	if n := e.count("memo"); n != 0 {
		t.Fatalf("%d memo row(s) after inbox use", n)
	}
}

// TestInboxTenantIsolation is docs/log/130 A-3, A-4 and A-5.
func TestInboxTenantIsolation(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b")
	type listResp struct {
		Items   []inboxItemWire  `json:"items"`
		Mailbox inboxMailboxWire `json:"mailbox"`
	}
	t1 := decode[listResp](t, e.do("b", "GET", "/api/inbox", nil, e.t1.Slug))
	if len(t1.Items) != 1 || t1.Mailbox.Unseen != 1 {
		t.Fatalf("A-3: b in T1: %+v", t1)
	}
	t2 := decode[listResp](t, e.do("b", "GET", "/api/inbox", nil, e.t2.Slug))
	if len(t2.Items) != 0 || t2.Mailbox.Unseen != 0 {
		t.Fatalf("A-3: b in T2 sees T1's item: %+v", t2)
	}
	for _, who := range []struct{ name, tenant string }{{"x", e.t2.Slug}, {"c", e.t1.Slug}, {"b", e.t2.Slug}} {
		for _, p := range []struct{ method, path string }{
			{"GET", "/api/inbox/items/" + id}, {"POST", "/api/inbox/items/" + id + "/read"},
			{"POST", "/api/inbox/items/" + id + "/claim"}, {"POST", "/api/inbox/items/" + id + "/decline"},
			{"POST", "/api/inbox/items/" + id + "/withdraw"}, {"POST", "/api/inbox/items/" + id + "/launch"},
		} {
			body := any(nil)
			if strings.HasSuffix(p.path, "/launch") {
				body = map[string]any{"dir": ""}
			}
			if rec := e.do(who.name, p.method, p.path, body, who.tenant); rec.Code != http.StatusNotFound {
				t.Errorf("A-4/A-5: %s (%s) %s %s: %d, want 404", who.name, who.tenant, p.method, p.path, rec.Code)
			}
		}
	}
	it, _, _ := e.st.GetInboxItem(context.Background(), id)
	if it.State != "open" {
		t.Fatalf("a non-participant changed the item: %+v", it)
	}
	if rec := e.do("x", "GET", "/api/inbox/items/no-such-id", nil, e.t2.Slug); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}

// TestInboxReceiveControls is docs/log/130 A-6 and A-7.
func TestInboxReceiveControls(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	sendTo := func(to ...string) *httptest.ResponseRecorder {
		var keys []string
		for _, n := range to {
			keys = append(keys, key(n))
		}
		return e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "request", "recipients": keys, "body": "x"})
	}
	// Chosen members only, without a.
	if rec := e.do("b", "PUT", "/api/inbox/settings", map[string]any{"acceptFrom": "chosen", "allowed": []string{key("c")}}); rec.Code != 200 {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	rec := sendTo("b", "c")
	er := decode[errResp](t, rec)
	if rec.Code != http.StatusForbidden || er.Error.Code != errCodeInboxNotAccepting || len(er.Error.Recipients) != 1 || er.Error.Recipients[0] != key("b") {
		t.Fatalf("A-6 chosen: %d %s", rec.Code, rec.Body.String())
	}
	if er.Error.Code == errCodeInboxInvalidRecipient {
		t.Fatal("A-6 must not reuse the invalid-recipient code")
	}
	// Chosen with a on the list: accepted.
	_ = e.do("b", "PUT", "/api/inbox/settings", map[string]any{"acceptFrom": "chosen", "allowed": []string{key("a")}})
	if rec := sendTo("b"); rec.Code != http.StatusCreated {
		t.Fatalf("chosen sender refused: %d %s", rec.Code, rec.Body.String())
	}
	// A block wins over the allow.
	if rec := e.do("b", "PUT", "/api/inbox/blocks/"+key("a"), nil); rec.Code != 200 {
		t.Fatalf("block: %d %s", rec.Code, rec.Body.String())
	}
	before := e.count("inbox_item")
	if rec := sendTo("c", "b"); rec.Code != http.StatusForbidden {
		t.Fatalf("A-6 block: %d %s", rec.Code, rec.Body.String())
	}
	if e.count("inbox_item") != before {
		t.Fatal("A-6: the blocked item was created for the other recipient")
	}
	if rec := e.do("b", "DELETE", "/api/inbox/blocks/"+key("a"), nil); rec.Code != 200 {
		t.Fatalf("unblock: %d", rec.Code)
	}
	// A-7: do-not-disturb still delivers.
	_ = e.do("c", "PUT", "/api/inbox/settings", map[string]any{"acceptFrom": "tenant", "dnd": true})
	if rec := sendTo("c"); rec.Code != http.StatusCreated {
		t.Fatalf("A-7: %d %s", rec.Code, rec.Body.String())
	}
	type listResp struct {
		Items   []inboxItemWire  `json:"items"`
		Mailbox inboxMailboxWire `json:"mailbox"`
	}
	l := decode[listResp](t, e.do("c", "GET", "/api/inbox", nil))
	if len(l.Items) != 1 || !l.Mailbox.DND || l.Items[0].Delivery != "unread" || !l.Items[0].CanClaim {
		t.Fatalf("A-7: c's view under do-not-disturb: %+v", l)
	}
}

// TestInboxSendLimitsAtTheHandler is docs/log/130 A-8: the recipient cap and the body cap on
// both sides, and the pair limit's answer.
func TestInboxSendLimitsAtTheHandler(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	ctx := context.Background()
	var keys []string
	for i := 0; i < 21; i++ {
		email := "m" + string(rune('a'+i)) + "@example.com"
		id, _ := e.st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
		if _, err := e.st.EnsureMembership(ctx, id.ID, e.t1.ID, "member"); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, sanitizeUser(email))
	}
	rec := e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "dm", "recipients": keys, "body": "x"})
	if rec.Code != http.StatusBadRequest || decode[errResp](t, rec).Error.Code != errCodeInboxTooManyRecipients {
		t.Fatalf("21 recipients: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "dm", "recipients": keys[:20], "body": "x"}); rec.Code != http.StatusCreated {
		t.Fatalf("20 recipients: %d %s", rec.Code, rec.Body.String())
	}
	at := strings.Repeat("é", inboxBodyMaxBytes/2)
	if rec := e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "dm", "recipients": []string{key("b")}, "body": at}); rec.Code != http.StatusCreated {
		t.Fatalf("body at the cap (%d bytes): %d %s", len(at), rec.Code, rec.Body.String())
	}
	over := at + "x"
	rec = e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "dm", "recipients": []string{key("b")}, "body": over})
	if rec.Code != http.StatusRequestEntityTooLarge || decode[errResp](t, rec).Error.Code != errCodeInboxBodyTooLarge {
		t.Fatalf("body one byte over: %d %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 9; i++ { // the at-the-cap DM above is pending #1 for a -> b
		e.send("a", "request", "b")
	}
	rec = e.do("a", "POST", "/api/inbox/items", map[string]any{"kind": "request", "recipients": []string{key("b")}, "body": "x"})
	if er := decode[errResp](t, rec); rec.Code != http.StatusTooManyRequests || er.Error.Code != errCodeInboxPendingLimit || er.Error.Recipients[0] != key("b") {
		t.Fatalf("11th pending: %d %s", rec.Code, rec.Body.String())
	}
}

// TestInboxClaimAnswers is docs/log/130 R-1's answer to the loser and R-4's double claim,
// R-5 on every final state, and A-10's audit rows without a body.
func TestInboxClaimAnswers(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b", "c")
	if rec := e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil); rec.Code != 200 {
		t.Fatalf("b claims: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.do("c", "POST", "/api/inbox/items/"+id+"/claim", nil)
	er := decode[errResp](t, rec)
	if rec.Code != http.StatusConflict || er.Error.State != "claimed" || er.Error.Assignee.UserKey != key("b") {
		t.Fatalf("R-1 loser: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	if rec.Code != 200 || decode[itemResp](t, rec).Item.Assignee.UserKey != key("b") {
		t.Fatalf("R-4: b's second claim: %d %s", rec.Code, rec.Body.String())
	}
	// c's view: taken, cannot claim, does not see b's delivery.
	got := decode[itemResp](t, e.do("c", "GET", "/api/inbox/items/"+id, nil)).Item
	if got.CanClaim || got.State != "claimed" || got.Recipients[0].Delivery != "" {
		t.Fatalf("c's view of a taken request: %+v", got)
	}
	if rec := e.do("b", "POST", "/api/inbox/items/"+id+"/done", nil); rec.Code != 200 {
		t.Fatalf("done: %d", rec.Code)
	}
	for _, op := range []struct{ who, op string }{{"b", "claim"}, {"c", "decline"}, {"b", "release"}, {"b", "done"}, {"a", "withdraw"}} {
		if rec := e.do(op.who, "POST", "/api/inbox/items/"+id+"/"+op.op, nil); rec.Code != http.StatusConflict ||
			decode[errResp](t, rec).Error.State != "done" {
			t.Errorf("R-5 %s on done: %d %s", op.op, rec.Code, rec.Body.String())
		}
	}
	rows, err := e.st.ListAuditByTenant(context.Background(), e.t1.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, r := range rows {
		actions[r.Action]++
		if strings.Contains(r.Detail, "flaky") || strings.Contains(r.Target, "flaky") {
			t.Fatalf("A-10: an audit row carries the body: %+v", r)
		}
	}
	if actions["inbox.send"] != 1 || actions["inbox.claim"] != 1 || actions["inbox.done"] != 1 {
		t.Fatalf("A-10: audit actions %v", actions)
	}
}

// TestInboxLaunchChecksClaimFirst is docs/log/130 R-6, R-7 and R-8 through the handler: only
// the assignee launches, the refusal comes before any workspace exists, the session is
// recorded on the request, and a second launch is refused without reaching the Agent.
func TestInboxLaunchChecksClaimFirst(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	var mu sync.Mutex
	creates := 0
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/sessions" {
			mu.Lock()
			creates++
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"s-1","dir":"/home/dev/repos/app","repo":"app"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(agent.Close)
	ws := store.Workspace{ID: "ws-b", TenantID: e.t1.ID, MembershipID: e.members["b"], ContainerName: "b",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	id := e.send("a", "request", "b", "c")
	launch := func(who, id string) *httptest.ResponseRecorder {
		return e.do(who, "POST", "/api/inbox/items/"+id+"/launch", map[string]any{"dir": "/home/dev/repos/app", "kind": "claude",
			"worktree": true, "new_branch": "fix-flaky"})
	}
	// Not claimed yet, and c is not the assignee: refused, nothing created.
	if rec := launch("b", id); rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.State != "open" {
		t.Fatalf("launch of an open request: %d %s", rec.Code, rec.Body.String())
	}
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	if rec := launch("c", id); rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeInboxStateConflict {
		t.Fatalf("R-7: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count("workspace"); n != 0 || creates != 0 {
		t.Fatalf("a refused launch created %d workspace(s) and %d session(s)", n, creates)
	}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	e.mgr.rts[e.members["b"]] = cachedRT{rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: ws}
	rec := launch("b", id)
	if rec.Code != http.StatusCreated {
		t.Fatalf("assignee launch: %d %s", rec.Code, rec.Body.String())
	}
	it, _, _ := e.st.GetInboxItem(context.Background(), id)
	if it.SessionName != "s-1" || it.SessionBranch != "fix-flaky" || it.SessionMembershipID != e.members["b"] {
		t.Fatalf("launch not recorded: %+v", it)
	}
	if rec := launch("b", id); rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.Code != errCodeInboxLaunchBusy {
		t.Fatalf("R-8: second launch: %d %s", rec.Code, rec.Body.String())
	}
	if creates != 1 {
		t.Fatalf("R-8: %d sessions created, want 1", creates)
	}
	// The sender sees that a session exists, not its name or path in b's workspace.
	if s := decode[itemResp](t, e.do("a", "GET", "/api/inbox/items/"+id, nil)).Item.Session; s == nil || s.Name != "" || s.Branch != "fix-flaky" {
		t.Fatalf("the sender's view of the session: %+v", s)
	}
	// R-6: withdrawn before the launch is pressed.
	id2 := e.send("a", "request", "b")
	_ = e.do("b", "POST", "/api/inbox/items/"+id2+"/claim", nil)
	_ = e.do("a", "POST", "/api/inbox/items/"+id2+"/withdraw", nil)
	if rec := launch("b", id2); rec.Code != http.StatusConflict || decode[errResp](t, rec).Error.State != "withdrawn" {
		t.Fatalf("R-6: %d %s", rec.Code, rec.Body.String())
	}
	if creates != 1 {
		t.Fatalf("R-6: the withdrawn request started a session")
	}
}

// TestInboxRemovalClosesStream is docs/log/130 M-5: a stream opened before the removal ends
// with it, and the next REST call is refused. It also checks that the stream frames the
// mailbox and moves when an item arrives.
func TestInboxRemovalClosesStream(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	srv := httptest.NewServer(e.mux)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("GET", srv.URL+"/api/inbox/stream", nil)
	req.Header.Set(inboxTestEmailHeader, "b@example.com")
	req.Header.Set("X-AF-Tenant", e.t1.Slug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	frames := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") {
				frames <- l
			}
		}
		close(frames)
	}()
	first := <-frames
	if !strings.Contains(first, `"stream":"inbox"`) || !strings.Contains(first, `"unseen":0`) {
		t.Fatalf("first frame %s", first)
	}
	e.send("a", "dm", "b")
	select {
	case f := <-frames:
		if !strings.Contains(f, `"unseen":1`) {
			t.Fatalf("frame after a DM: %s", f)
		}
	case <-time.After(2 * inboxStreamTick):
		t.Fatal("no frame after a DM arrived")
	}
	// What RemoveMembership does: deactivate, then close the member's connections.
	if err := e.st.SetMembershipStatus(context.Background(), e.members["b"], "inactive"); err != nil {
		t.Fatal(err)
	}
	if n := e.mgr.memberConns.cancel(e.members["b"]); n != 1 {
		t.Fatalf("closed %d tracked connection(s), want the stream", n)
	}
	select {
	case _, ok := <-frames:
		for ok {
			_, ok = <-frames
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream outlived the removal")
	}
	if rec := e.do("b", "GET", "/api/inbox", nil, e.t1.Slug); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("REST after removal: %d", rec.Code)
	}
}

// TestInboxRemovalReconcilesOnRead: a removed assignee's claim is back to open the next time
// anyone reads, without waiting for the sweep (docs/log/130 M-1 as A and C see it).
func TestInboxRemovalReconcilesOnRead(t *testing.T) {
	t.Parallel()
	e := newInboxEnv(t)
	id := e.send("a", "request", "b", "c")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)
	_ = e.st.SetMembershipStatus(context.Background(), e.members["b"], "inactive")
	got := decode[itemResp](t, e.do("c", "GET", "/api/inbox/items/"+id, nil)).Item
	if got.State != "open" || got.Assignee != nil || !got.CanClaim {
		t.Fatalf("c after b's removal: %+v", got)
	}
	sender := decode[itemResp](t, e.do("a", "GET", "/api/inbox/items/"+id, nil)).Item
	var removed bool
	for _, r := range sender.Recipients {
		if r.Removed && !r.Eligible {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("the sender does not see the removed recipient as unable to take it: %+v", sender.Recipients)
	}
}
