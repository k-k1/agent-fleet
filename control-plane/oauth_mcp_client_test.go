package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ADR 0119 P2, Control Plane half. What is pinned: the state is single-use however the
// callback ends, the browser must be the member who started the sign-in, the code goes only
// to that member's Agent, and the redirect back to the Console carries the outcome and
// nothing the browser sent.

type mcpClientAgentCall struct {
	method, path string
	body         map[string]string
}

// mcpClientAgent answers the three OAuth routes of the Agent and records every call.
type mcpClientAgent struct {
	srv *httptest.Server
	mu  sync.Mutex
	// calls are in arrival order.
	calls []mcpClientAgentCall
	// startStatus/startBody and callbackStatus/callbackBody are the canned answers.
	startStatus, callbackStatus int
	startBody, callbackBody     string
	disconnectBody              string
}

func newMCPClientAgent(t *testing.T) *mcpClientAgent {
	a := &mcpClientAgent{
		startStatus: 200, callbackStatus: 200,
		startBody:      `{"authorizeUrl":"https://as.example.com/authorize?x=1","serverName":"hosted","issuer":"https://as.example.com","registration":"cimd","scope":"mcp:read"}`,
		callbackBody:   `{"serverId":"mcp-1","serverName":"hosted","issuer":"https://as.example.com","registration":"cimd"}`,
		disconnectBody: `{"serverId":"mcp-1","issuer":"https://as.example.com","hadGrant":true,"revoked":true}`,
	}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := mcpClientAgentCall{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(b, &c.body)
		a.mu.Lock()
		a.calls = append(a.calls, c)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/start"):
			w.WriteHeader(a.startStatus)
			_, _ = w.Write([]byte(a.startBody))
		case strings.HasSuffix(r.URL.Path, "/oauth/callback"):
			w.WriteHeader(a.callbackStatus)
			_, _ = w.Write([]byte(a.callbackBody))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(a.disconnectBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *mcpClientAgent) snapshot() []mcpClientAgentCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]mcpClientAgentCall(nil), a.calls...)
}

func (a *mcpClientAgent) callsTo(suffix string) []mcpClientAgentCall {
	var out []mcpClientAgentCall
	for _, c := range a.snapshot() {
		if strings.HasSuffix(c.path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

type mcpClientMember struct {
	ident   store.Identity
	mem     store.Membership
	session *http.Cookie
	agent   *mcpClientAgent
	email   string
	sub     string
}

type mcpClientFixture struct {
	cfg        config
	st         *store.SQL
	alice, bob *mcpClientMember
}

func newMCPClientFixture(t *testing.T) *mcpClientFixture {
	t.Helper()
	ctx := context.Background()
	idp := newStubIdP(t, &stubIdP{})
	st := p3Store(t)
	mgr := p4Manager(t, st)
	mgr.publicBaseURL = "https://af.example.com"
	cfg := oauthTestConfig(t, stubProvider(auth.GoogleProviderID, idp, auth.TrustEmailVerified))
	cfg.mgr = mgr
	for _, p := range cfg.providers {
		if op, ok := p.(*auth.OIDCProvider); ok {
			op.DeployAllowed, op.DBAllowed = cfg.emailAllowed, cfg.tenantEmailAllowed
		}
	}
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	f := &mcpClientFixture{cfg: cfg, st: st}
	mk := func(email, sub string) *mcpClientMember {
		ident, _, err := st.LinkIdentity(ctx, linkOf(auth.GoogleProviderID, sub, email, true))
		if err != nil {
			t.Fatal(err)
		}
		mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		ws := store.Workspace{
			ID: "W-" + sub, TenantID: tn.ID, MembershipID: mem.ID, ContainerName: "af-ws-" + sub,
			DataDir: "/srv/" + sub, AgentPort: "7731", AgentToken: "tok", State: "running", CreatedAt: store.NowTS(),
		}
		if err := st.CreateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		agent := newMCPClientAgent(t)
		mgr.rts[mem.ID] = cachedRT{rt: stubRuntime{endpoint: agent.srv.URL, token: "tok"}, ws: ws}
		b, _ := json.Marshal(sessionClaims{
			Email: email, Exp: time.Now().Add(time.Hour).Unix(), Prov: auth.GoogleProviderID, Sub: sub,
		})
		return &mcpClientMember{
			ident: ident, mem: mem, agent: agent, email: email, sub: sub,
			session: &http.Cookie{Name: sessionCookie, Value: cfg.signCookie(b)},
		}
	}
	f.alice = mk("alice@acme.co.jp", "g-alice")
	f.bob = mk("bob@acme.co.jp", "g-bob")
	return f
}

// asMember stamps what authGate stamps for a signed-in request: the email header and the
// proven login, so the handler resolves this member.
func (f *mcpClientFixture) asMember(r *http.Request, m *mcpClientMember) *http.Request {
	r.Header.Set(f.cfg.mgr.emailHeader, m.email)
	return r.WithContext(withLoginRef(r.Context(), loginRef{auth.GoogleProviderID, m.sub}))
}

func (f *mcpClientFixture) start(t *testing.T, m *mcpClientMember, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp-servers/mcp-1/oauth/start", strings.NewReader(body))
	r.SetPathValue("id", "mcp-1")
	r = f.asMember(r, m)
	w := httptest.NewRecorder()
	newAgentProxyAPI(f.cfg.mgr).withResolved(f.cfg.handleMCPClientOAuthStart)(w, r)
	state := ""
	if calls := m.agent.callsTo("/oauth/start"); len(calls) > 0 {
		state = calls[len(calls)-1].body["state"]
	}
	return w, state
}

func (f *mcpClientFixture) callback(t *testing.T, session *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/oauth2/mcp/callback?"+query, nil)
	if session != nil {
		r.AddCookie(session)
	}
	w := httptest.NewRecorder()
	f.cfg.handleMCPClientOAuthCallback(w, r)
	return w
}

func mcpClientAuditRows(t *testing.T, st *store.SQL) []store.AuditLog {
	t.Helper()
	var out []store.AuditLog
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT actor_id, action, target, detail FROM audit_log WHERE action='mcp.client_oauth' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a store.AuditLog
		if err := rows.Scan(&a.ActorID, &a.Action, &a.Target, &a.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func mcpClientLocation(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (%s)", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMCPClientOAuthStartRecordsTheStateAndAsksTheAgent(t *testing.T) {
	f := newMCPClientFixture(t)
	// A member-chosen redirect or client must not reach the Agent: the body may carry the
	// registration path and a scope, nothing else.
	w, state := f.start(t, f.alice, `{"registration":"dcr","scope":"mcp:write","redirectUri":"https://evil.example.com/cb","clientName":"evil"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["authorize_url"] != "https://as.example.com/authorize?x=1" || resp["issuer"] != "https://as.example.com" || resp["registration"] != "cimd" {
		t.Errorf("response = %v", resp)
	}
	calls := f.alice.agent.callsTo("/oauth/start")
	if len(calls) != 1 {
		t.Fatalf("agent calls = %d", len(calls))
	}
	b := calls[0].body
	if len(state) != 64 || b["redirectUri"] != "https://af.example.com/oauth2/mcp/callback" ||
		b["cimdUrl"] != "https://af.example.com/oauth/mcp-client.json" || b["registration"] != "dcr" ||
		b["scope"] != "mcp:write" || b["clientName"] != "Agent Fleet" {
		t.Errorf("agent body = %v", b)
	}
	if calls[0].path != "/mcp-servers/mcp-1/oauth/start" {
		t.Errorf("path = %s", calls[0].path)
	}
	if len(f.bob.agent.snapshot()) != 0 {
		t.Error("another member's Agent was called")
	}
	// The row binds member, membership, workspace and a 10-minute expiry, under a hash.
	st, ok, expired, err := f.st.TakeMCPClientOAuthState(context.Background(), state, time.Now().Unix())
	if err != nil || !ok || expired {
		t.Fatalf("state row: ok=%v expired=%v err=%v", ok, expired, err)
	}
	if st.IdentityID != f.alice.ident.ID || st.MembershipID != f.alice.mem.ID || st.WorkspaceID != "W-g-alice" ||
		st.ServerID != "mcp-1" || st.Issuer != "https://as.example.com" {
		t.Errorf("state = %+v", st)
	}
	if d := st.ExpiresAt - time.Now().Unix(); d < 590 || d > 600 {
		t.Errorf("expiry in %ds, want 600", d)
	}
	rows := mcpClientAuditRows(t, f.st)
	if len(rows) != 1 || rows[0].ActorID != f.alice.ident.ID || rows[0].Target != "hosted" ||
		!strings.Contains(rows[0].Detail, "event=started") || !strings.Contains(rows[0].Detail, "issuer=https://as.example.com") ||
		!strings.Contains(rows[0].Detail, "server=mcp-1") {
		t.Errorf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Detail, state) {
		t.Error("the state must not be audited")
	}
}

func TestMCPClientOAuthStartRelaysAgentRefusals(t *testing.T) {
	f := newMCPClientFixture(t)
	f.alice.agent.startStatus = 409
	f.alice.agent.startBody = `{"error":{"code":"client_id_needed","message":"enter one","issuer":"https://as.example.com","stray":"x"}}`
	w, state := f.start(t, f.alice, ``)
	if w.Code != 409 {
		t.Fatalf("status = %d", w.Code)
	}
	var e struct{ Error map[string]string }
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error["code"] != "client_id_needed" || e.Error["issuer"] != "https://as.example.com" || e.Error["stray"] != "" {
		t.Errorf("error = %v", e.Error)
	}
	if _, ok, _, _ := f.st.TakeMCPClientOAuthState(context.Background(), state, time.Now().Unix()); ok {
		t.Error("a refused start must leave no state behind")
	}
	if len(mcpClientAuditRows(t, f.st)) != 0 {
		t.Error("a refused start is not audited as started")
	}
}

func TestMCPClientOAuthStartNeedsAPublicBaseURL(t *testing.T) {
	f := newMCPClientFixture(t)
	f.cfg.mgr.publicBaseURL = ""
	w, _ := f.start(t, f.alice, ``)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no_public_base_url") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(f.alice.agent.snapshot()) != 0 {
		t.Error("the Agent must not be asked without a callback URL")
	}
}

func TestMCPClientOAuthStartWithoutHTTPSOffersNoCIMD(t *testing.T) {
	f := newMCPClientFixture(t)
	f.cfg.mgr.publicBaseURL = "http://127.0.0.1:8080"
	f.start(t, f.alice, ``)
	if got := f.alice.agent.callsTo("/oauth/start")[0].body["cimdUrl"]; got != "" {
		t.Errorf("cimdUrl = %q — CIMD needs an https client_id", got)
	}
}

func TestMCPClientOAuthCallbackCompletes(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	w := f.callback(t, f.alice.session, "code=THE-AUTH-CODE&iss="+url.QueryEscape("https://as.example.com")+"&state="+state)
	loc := mcpClientLocation(t, w)
	if loc.Path != "/" || loc.Host != "" || loc.Scheme != "" || loc.Query().Get("mcp_oauth") != "ok" ||
		loc.Query().Get("server") != "mcp-1" || loc.Query().Get("registration") != "cimd" {
		t.Errorf("Location = %s", loc)
	}
	if strings.Contains(loc.String(), "THE-AUTH-CODE") || strings.Contains(loc.String(), state) {
		t.Errorf("the redirect carries a code or state: %s", loc)
	}
	cb := f.alice.agent.callsTo("/oauth/callback")
	if len(cb) != 1 || cb[0].path != "/mcp-servers/mcp-1/oauth/callback" ||
		cb[0].body["code"] != "THE-AUTH-CODE" || cb[0].body["iss"] != "https://as.example.com" || cb[0].body["state"] != state {
		t.Errorf("forwarded = %+v", cb)
	}
	rows := mcpClientAuditRows(t, f.st)
	if len(rows) != 2 || !strings.Contains(rows[1].Detail, "event=completed") || !strings.Contains(rows[1].Detail, "issuer=https://as.example.com") {
		t.Errorf("audit = %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.Detail, "THE-AUTH-CODE") || strings.Contains(r.Detail, state) {
			t.Errorf("audit carries a secret: %s", r.Detail)
		}
	}
}

func TestMCPClientOAuthStateIsSingleUse(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	q := "code=c&state=" + state
	if loc := mcpClientLocation(t, f.callback(t, f.alice.session, q)); loc.Query().Get("mcp_oauth") != "ok" {
		t.Fatalf("first callback: %s", loc)
	}
	loc := mcpClientLocation(t, f.callback(t, f.alice.session, q))
	if loc.Query().Get("mcp_oauth") != "failed" || loc.Query().Get("reason") != "state_unknown" {
		t.Errorf("replay: %s", loc)
	}
	if n := len(f.alice.agent.callsTo("/oauth/callback")); n != 1 {
		t.Errorf("the Agent saw %d callbacks, want 1", n)
	}
}

// A callback that fails still burns the state: a second try with it must find nothing.
func TestMCPClientOAuthFailedCallbackAlsoSpendsTheState(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	f.alice.agent.callbackStatus = 400
	f.alice.agent.callbackBody = `{"error":{"code":"iss_mismatch","message":"x","issuer":"https://as.example.com","registration":"cimd"}}`
	loc := mcpClientLocation(t, f.callback(t, f.alice.session, "code=c&iss=https%3A%2F%2Fevil.example.com&state="+state))
	if loc.Query().Get("reason") != "iss_mismatch" {
		t.Errorf("Location = %s", loc)
	}
	f.alice.agent.callbackStatus, f.alice.agent.callbackBody = 200, `{"issuer":"https://as.example.com"}`
	loc = mcpClientLocation(t, f.callback(t, f.alice.session, "code=c&state="+state))
	if loc.Query().Get("reason") != "state_unknown" {
		t.Errorf("retry: %s", loc)
	}
	rows := mcpClientAuditRows(t, f.st)
	if last := rows[len(rows)-1]; !strings.Contains(last.Detail, "event=failed") || !strings.Contains(last.Detail, "reason=iss_mismatch") {
		t.Errorf("audit = %+v", rows)
	}
}

// Member B's browser holding member A's state must fail, and must not deliver the code to
// anyone's Agent as a completion.
func TestMCPClientOAuthCrossMemberCallbackFails(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	loc := mcpClientLocation(t, f.callback(t, f.bob.session, "code=ALICES-CODE&state="+state))
	if loc.Query().Get("mcp_oauth") != "failed" || loc.Query().Get("reason") != "session" {
		t.Fatalf("Location = %s", loc)
	}
	for _, c := range append(f.alice.agent.callsTo("/oauth/callback"), f.bob.agent.callsTo("/oauth/callback")...) {
		if c.body["code"] != "" {
			t.Errorf("the code was forwarded: %+v", c)
		}
	}
	if len(f.bob.agent.snapshot()) != 0 {
		t.Error("Bob's Agent was called")
	}
	// The state is burnt: Alice herself cannot use it afterwards.
	loc = mcpClientLocation(t, f.callback(t, f.alice.session, "code=c&state="+state))
	if loc.Query().Get("reason") != "state_unknown" {
		t.Errorf("Alice after the refusal: %s", loc)
	}
	rows := mcpClientAuditRows(t, f.st)
	if last := rows[len(rows)-1]; last.ActorID != f.alice.ident.ID || !strings.Contains(last.Detail, "reason=session") {
		t.Errorf("audit = %+v", rows)
	}
}

func TestMCPClientOAuthCallbackWithoutASessionFails(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	loc := mcpClientLocation(t, f.callback(t, nil, "code=c&state="+state))
	if loc.Query().Get("reason") != "session" {
		t.Errorf("Location = %s", loc)
	}
	if len(f.alice.agent.callsTo("/oauth/callback")) != 1 { // only the abandon notice
		t.Errorf("calls = %+v", f.alice.agent.snapshot())
	}
	if c := f.alice.agent.callsTo("/oauth/callback")[0]; c.body["code"] != "" || c.body["error"] == "" {
		t.Errorf("the Agent must be told to drop the verifier, not given a code: %+v", c)
	}
}

func TestMCPClientOAuthExpiredStateFails(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	// Age the row past its expiry.
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE mcp_client_oauth_state SET expires_at=?`, time.Now().Unix()-1); err != nil {
		t.Fatal(err)
	}
	loc := mcpClientLocation(t, f.callback(t, f.alice.session, "code=c&state="+state))
	if loc.Query().Get("reason") != "state_expired" {
		t.Errorf("Location = %s", loc)
	}
	for _, c := range f.alice.agent.callsTo("/oauth/callback") {
		if c.body["code"] != "" {
			t.Error("an expired state must not forward the code")
		}
	}
	if n := countRows(t, f.st, "mcp_client_oauth_state"); n != 0 {
		t.Errorf("rows left = %d", n)
	}
}

func TestMCPClientOAuthUnknownOrMissingState(t *testing.T) {
	f := newMCPClientFixture(t)
	for _, q := range []string{"code=c", "code=c&state=", "code=c&state=never-issued", "code=c&state=" + strings.Repeat("a", 9000)} {
		loc := mcpClientLocation(t, f.callback(t, f.alice.session, q))
		if loc.Query().Get("reason") != "state_unknown" {
			t.Errorf("%.40s: Location = %s", q, loc)
		}
	}
	if len(f.alice.agent.snapshot()) != 0 {
		t.Error("nothing may reach an Agent for a state nobody issued")
	}
}

// The redirect is a fixed origin-relative path. Nothing the browser sends picks where it
// goes, and nothing the Agent (or the authorization server behind it) says is echoed.
func TestMCPClientOAuthRedirectCannotBeSteered(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	f.alice.agent.callbackStatus = 502
	f.alice.agent.callbackBody = `{"error":{"code":"https://evil.example.com/x","message":"see https://evil.example.com","registration":"//evil.example.com","issuer":"https://evil.example.com"}}`
	q := "code=c&state=" + state +
		"&next=https%3A%2F%2Fevil.example.com&redirect=//evil.example.com&redirect_uri=https%3A%2F%2Fevil.example.com" +
		"&return_to=%2F%2Fevil.example.com&server=https%3A%2F%2Fevil.example.com&reason=https%3A%2F%2Fevil.example.com"
	w := f.callback(t, f.alice.session, q)
	raw := w.Header().Get("Location")
	if !strings.HasPrefix(raw, "/?") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "evil") {
		t.Fatalf("Location = %q", raw)
	}
	loc := mcpClientLocation(t, w)
	if loc.Host != "" || loc.Scheme != "" || loc.Query().Get("reason") != "failed" || loc.Query().Get("server") != "mcp-1" {
		t.Errorf("Location = %s", loc)
	}
	if loc.Query().Get("registration") != "cimd" {
		t.Errorf("an unknown registration must fall back to the recorded one: %s", loc)
	}
}

func TestMCPClientOAuthProviderErrorForwardsNoText(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	f.alice.agent.callbackStatus = 400
	f.alice.agent.callbackBody = `{"error":{"code":"authorization_denied","message":"no"}}`
	w := f.callback(t, f.alice.session, "error=access_denied&error_description=EVIL-TEXT&code=STRAY&state="+state)
	loc := mcpClientLocation(t, w)
	if loc.Query().Get("reason") != "authorization_denied" {
		t.Errorf("Location = %s", loc)
	}
	cb := f.alice.agent.callsTo("/oauth/callback")
	if len(cb) != 1 || cb[0].body["error"] == "" || cb[0].body["code"] != "" {
		t.Errorf("forwarded = %+v", cb)
	}
	for _, c := range f.alice.agent.snapshot() {
		for _, v := range c.body {
			if strings.Contains(v, "EVIL-TEXT") {
				t.Error("the authorization server's text was forwarded")
			}
		}
	}
	if strings.Contains(w.Header().Get("Location"), "EVIL-TEXT") {
		t.Error("the authorization server's text reached the redirect")
	}
}

func TestMCPClientOAuthCallbackWithADeadAgentFails(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	f.alice.agent.srv.Close()
	loc := mcpClientLocation(t, f.callback(t, f.alice.session, "code=c&state="+state))
	if loc.Query().Get("reason") != "workspace_unreachable" {
		t.Errorf("Location = %s", loc)
	}
}

func TestMCPClientOAuthDisconnectIsAudited(t *testing.T) {
	f := newMCPClientFixture(t)
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/mcp-servers/mcp-1/oauth", nil)
		r.SetPathValue("id", "mcp-1")
		r = f.asMember(r, f.alice)
		w := httptest.NewRecorder()
		newAgentProxyAPI(f.cfg.mgr).withResolved(f.cfg.handleMCPClientOAuthDisconnect)(w, r)
		return w
	}
	if w := do(); w.Code != 200 || !strings.Contains(w.Body.String(), `"revoked":true`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := mcpClientAuditRows(t, f.st)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "event=disconnected") ||
		!strings.Contains(rows[0].Detail, "issuer=https://as.example.com") || rows[0].ActorID != f.alice.ident.ID {
		t.Fatalf("audit = %+v", rows)
	}
	f.alice.agent.disconnectBody = `{"serverId":"mcp-1","hadGrant":false,"revoked":false}`
	do()
	if n := len(mcpClientAuditRows(t, f.st)); n != 1 {
		t.Errorf("disconnecting nothing must not be audited (rows = %d)", n)
	}
}

func TestMCPClientOAuthCIMDDocument(t *testing.T) {
	f := newMCPClientFixture(t)
	r := httptest.NewRequest(http.MethodGet, "https://af.example.com/oauth/mcp-client.json", nil)
	w := httptest.NewRecorder()
	f.cfg.handleMCPClientOAuthCIMD(w, r)
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if doc["client_id"] != "https://af.example.com/oauth/mcp-client.json" || doc["client_id"] != r.URL.String() {
		t.Errorf("client_id must be the document's own URL: %v", doc["client_id"])
	}
	if doc["client_name"] != "Agent Fleet" || doc["token_endpoint_auth_method"] != "none" ||
		strings.Join(mcpClientStrings(doc["redirect_uris"]), ",") != "https://af.example.com/oauth2/mcp/callback" ||
		strings.Join(mcpClientStrings(doc["grant_types"]), ",") != "authorization_code,refresh_token" {
		t.Errorf("doc = %v", doc)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", w.Header().Get("Content-Type"))
	}
	// The brand names the client.
	f.cfg.brand = newBrandResolver(newBrandConfig("orange", "staging"), nil)
	w = httptest.NewRecorder()
	f.cfg.handleMCPClientOAuthCIMD(w, r)
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	if doc["client_name"] != "[staging] Agent Fleet" {
		t.Errorf("client_name = %v", doc["client_name"])
	}
	// No https base URL, no document.
	f.cfg.mgr.publicBaseURL = "http://af.example.com"
	w = httptest.NewRecorder()
	f.cfg.handleMCPClientOAuthCIMD(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d without https", w.Code)
	}
}

func mcpClientStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// Exactly one path of /oauth/ is public. The callback rides the /oauth2/ prefix.
func TestMCPClientOAuthRoutesAndExemptions(t *testing.T) {
	_, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	for path, want := range map[string]bool{
		"/oauth/mcp-client.json":         true,
		"/oauth/mcp-client.json/x":       false,
		"/oauth/":                        false,
		"/oauth/other":                   false,
		"/oauth":                         false,
		"/oauth2/mcp/callback":           true,
		"/api/mcp-servers/x/oauth/start": false,
	} {
		if got := isAuthExempt(path); got != want {
			t.Errorf("isAuthExempt(%q) = %v, want %v", path, got, want)
		}
	}
	have := map[string]bool{}
	for _, line := range muxRoutes(t, mux) {
		have[line] = true
	}
	for _, want := range []string{
		"POST /api/mcp-servers/{id}/oauth/start", "GET /api/mcp-servers/{id}/oauth",
		"DELETE /api/mcp-servers/{id}/oauth", "PUT /api/mcp-servers/{id}/oauth/client",
		"GET /oauth2/mcp/callback", "GET /oauth/mcp-client.json",
	} {
		if !have[want] {
			t.Errorf("route %q is not registered", want)
		}
	}
	// The Agent's own callback is not reachable from the Console.
	for line := range have {
		if strings.Contains(line, "/oauth/callback") {
			t.Errorf("the Agent callback is proxied: %s", line)
		}
	}
}

// Under AUTH=proxy there is no session cookie: the gateway's identity header is the proof, and
// the same binding applies.
func TestMCPClientOAuthCallbackUnderAuthProxy(t *testing.T) {
	f := newMCPClientFixture(t)
	f.cfg.mgr.authMode = "proxy"
	call := func(state, email string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/oauth2/mcp/callback?code=c&state="+state, nil)
		if email != "" {
			r.Header.Set(f.cfg.mgr.emailHeader, email)
		}
		w := httptest.NewRecorder()
		f.cfg.handleMCPClientOAuthCallback(w, r)
		return w
	}
	_, state := f.start(t, f.alice, ``)
	if loc := mcpClientLocation(t, call(state, f.bob.email)); loc.Query().Get("reason") != "session" {
		t.Errorf("Bob with Alice's state: %s", loc)
	}
	_, state = f.start(t, f.alice, ``)
	if loc := mcpClientLocation(t, call(state, "")); loc.Query().Get("reason") != "session" {
		t.Errorf("no gateway identity must fail closed: %s", loc)
	}
	_, state = f.start(t, f.alice, ``)
	if loc := mcpClientLocation(t, call(state, f.alice.email)); loc.Query().Get("mcp_oauth") != "ok" {
		t.Errorf("Alice: %s", loc)
	}
}

// A session whose provider is gone is not enough: the exempt path skips
// authGate's per-request re-check, so the callback repeats it.
func TestMCPClientOAuthCallbackRechecksTheSessionIsStillAllowed(t *testing.T) {
	f := newMCPClientFixture(t)
	_, state := f.start(t, f.alice, ``)
	// The operator removed the IdP the session was minted by.
	b, _ := json.Marshal(sessionClaims{
		Email: f.alice.email, Exp: time.Now().Add(time.Hour).Unix(), Prov: "retired-idp", Sub: f.alice.sub,
	})
	gone := &http.Cookie{Name: sessionCookie, Value: f.cfg.signCookie(b)}
	loc := mcpClientLocation(t, f.callback(t, gone, "code=c&state="+state))
	if loc.Query().Get("reason") != "session" {
		t.Errorf("Location = %s", loc)
	}
	for _, c := range f.alice.agent.callsTo("/oauth/callback") {
		if c.body["code"] != "" {
			t.Error("the code was forwarded for a session that is no longer allowed")
		}
	}
}
