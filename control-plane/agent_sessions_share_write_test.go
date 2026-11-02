package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TestInboxLaunchInvalidatesSharedSessions: the inbox launch is registered through withMembership;
// its completed write must invalidate the shared read, so the next list read sees the new session.
func TestInboxLaunchInvalidatesSharedSessions(t *testing.T) {
	e := newInboxEnv(t)
	var reads atomic.Int64
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"s-1","dir":"/home/dev/repos/app","repo":"app"}`)
		case r.Method == "GET" && r.URL.Path == "/sessions":
			reads.Add(1)
			_, _ = io.WriteString(w, sharedOneSession)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(agent.Close)
	ws := store.Workspace{ID: "ws-b", TenantID: e.t1.ID, MembershipID: e.members["b"], ContainerName: "b",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	rt := stubRuntime{endpoint: agent.URL, token: "tok"}
	e.mgr.rts[e.members["b"]] = cachedRT{rt: rt, ws: ws}

	h := e.mgr.invalidatesSessionsOnWrite(e.mux)
	id := e.send("a", "request", "b", "c")
	_ = e.do("b", "POST", "/api/inbox/items/"+id+"/claim", nil)

	read := func() { _, _, _ = e.mgr.agentSessionsShared(context.Background(), rt) }
	read()
	read()
	if reads.Load() != 1 {
		t.Fatalf("reads = %d before the write, want 1 (shared)", reads.Load())
	}
	// A GET through the wrapper must not drop the cache.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/inbox/items/"+id, nil))
	read()
	if reads.Load() != 1 {
		t.Fatalf("reads = %d after a GET, want 1", reads.Load())
	}
	req := httptest.NewRequest("POST", "/api/inbox/items/"+id+"/launch",
		strings.NewReader(`{"dir":"/home/dev/repos/app","kind":"claude"}`))
	req.Header.Set(inboxTestEmailHeader, "b@example.com")
	req.Header.Set("X-AF-Tenant", e.t1.Slug)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body.String())
	}
	read()
	if reads.Load() != 2 {
		t.Fatalf("reads = %d after the launch, want 2 (cache dropped)", reads.Load())
	}
}

// A refused write must not flush the shared read, on either listener: anonymous POSTs would
// otherwise defeat the sharing for every workspace. A write that got past routing and auth, or
// failed after partial success, still does.
func TestRefusedWritesKeepSharedSessions(t *testing.T) {
	srv, n := countingAgent(t, func(int64) (int, string) { return 200, sharedOneSession })
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	read := func() { _, _, _ = cfg.mgr.agentSessionsShared(context.Background(), rt) }
	mux.HandleFunc("POST /test/ok", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /test/boom", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	listeners := map[string]http.Handler{
		"main":      cfg.mgr.invalidatesSessionsOnWrite(mux),
		"workspace": workspaceListenerHandler(mux, cfg.mgr),
	}
	for name, h := range listeners {
		for _, path := range []string{"/internal/memos", "/no-such-route"} {
			cfg.mgr.sessionsShare.invalidate()
			n.Store(0)
			read()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
			if rec.Code != 401 && rec.Code != 404 {
				t.Fatalf("%s %s: %d, want a refusal", name, path, rec.Code)
			}
			read()
			if n.Load() != 1 {
				t.Errorf("%s: refused POST %s dropped the shared read (reads=%d)", name, path, n.Load())
			}
		}
	}
	// Positive control: a handled write, and a 5xx, invalidate (main listener; the workspace
	// listener only serves its own routes).
	for _, path := range []string{"/test/ok", "/test/boom"} {
		cfg.mgr.sessionsShare.invalidate()
		n.Store(0)
		read()
		rec := httptest.NewRecorder()
		listeners["main"].ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		read()
		if n.Load() != 2 {
			t.Errorf("POST %s did not invalidate (reads=%d)", path, n.Load())
		}
	}
}

// Public POSTs that authGate lets through anonymously must not flush the cache whatever status they
// answer (302 legacy redirect, 400/200 MCP-OAuth, 503 git without a base URL).
func TestPublicWritesKeepSharedSessions(t *testing.T) {
	srv, n := countingAgent(t, func(int64) (int, string) { return 200, sharedOneSession })
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	read := func() { _, _, _ = cfg.mgr.agentSessionsShared(context.Background(), rt) }
	mux.HandleFunc("POST /oauth2/test-ok", func(w http.ResponseWriter, r *http.Request) {})
	listeners := map[string]http.Handler{
		"main":      cfg.mgr.invalidatesSessionsOnWrite(mux),
		"workspace": workspaceListenerHandler(mux, cfg.mgr),
	}
	for name, h := range listeners {
		for _, path := range []string{"/agent-fleet/", "/mcp-oauth/token", "/mcp-oauth/revoke",
			"/git/t/r.git/git-upload-pack", "/oauth2/test-ok", "/login/x"} {
			cfg.mgr.sessionsShare.invalidate()
			n.Store(0)
			read()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", path, nil))
			read()
			if n.Load() != 1 {
				t.Errorf("%s: public POST %s dropped the shared read (reads=%d)", name, path, n.Load())
			}
		}
	}
	for _, p := range []string{"/mcp", "/internal/memos", "/api/sessions"} {
		if !writeMayChangeSessions(p) {
			t.Errorf("writeMayChangeSessions(%q) = false, want true", p)
		}
	}
}

// The mux redirects an unclean path (//, /./, /../) before the route's own auth runs, and the
// middleware sits behind authGate, which exempts /internal/ and /mcp/ by raw path. Run with the
// MCP OAuth issuer enabled so /mcp-oauth/token and revoke really answer 400 and not 404.
func TestAnonymousRoutingAndOAuthPostsKeepSharedSessions(t *testing.T) {
	t.Setenv("AF_MCP_ENABLED", "true")
	t.Setenv("AF_MCP_OAUTH", "1")
	srv, n := countingAgent(t, func(int64) (int, string) { return 200, sharedOneSession })
	cfg, _ := smokeEnvWith(t, allRouteSwitches(t)...)
	cfg.publicBaseURL = "https://af.example"
	cfg.mgr.publicBaseURL = "https://af.example"
	mux := buildMux(cfg)
	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	read := func() { _, _, _ = cfg.mgr.agentSessionsShared(context.Background(), rt) }
	h := cfg.authGate(cfg.mgr.invalidatesSessionsOnWrite(mux))
	cases := []struct {
		path string
		want func(int) bool
	}{
		{"/internal//memos", func(c int) bool { return c >= 300 && c < 400 }},
		{"/internal/./memos", func(c int) bool { return c >= 300 && c < 400 }},
		{"/internal/memos/../memos", func(c int) bool { return c >= 300 && c < 400 }},
		{"/mcp//memory", func(c int) bool { return c >= 300 && c < 400 }},
		{"/mcp-oauth/token", func(c int) bool { return c == 400 }},
		{"/mcp-oauth/revoke", func(c int) bool { return c == 400 || c == 200 }},
	}
	for _, tc := range cases {
		cfg.mgr.sessionsShare.invalidate()
		n.Store(0)
		read()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", tc.path, nil))
		if !tc.want(rec.Code) {
			t.Errorf("POST %s: status %d is not the case under test", tc.path, rec.Code)
		}
		read()
		if n.Load() != 1 {
			t.Errorf("POST %s (%d) dropped the shared read (reads=%d)", tc.path, rec.Code, n.Load())
		}
	}
}
