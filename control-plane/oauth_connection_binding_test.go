package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The Jira and Bitbucket connection callbacks are browser redirects served at
// /api/oauth/{provider}/callback behind authGate (the tests drive them through it, as routes.go
// registers them). What is pinned: the browser session must be the member who started the
// flow, the tokens go to the workspace of the membership recorded at start, that membership's
// tenant rules apply to the current session and address, and a refused callback exchanges and
// stores nothing.

type connProvider struct {
	name     string // gitOAuth provider id
	flows    *bbFlowRegistry
	start    func(config, http.ResponseWriter, *http.Request)
	callback func(config, http.ResponseWriter, *http.Request)
	savePath string
}

var connProviders = []connProvider{
	{"jira", jiraFlows, config.handleJiraOAuthStart, config.handleJiraOAuthCallback, "/connections/jira/oauth"},
	{"bitbucket", bbFlows, config.handleBitbucketOAuthStart, config.handleBitbucketOAuthCallback, "/connections/git/bitbucket/oauth"},
}

// connTokenServer stands in for the provider's token endpoint and counts exchanges.
func connTokenServer(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	oldJ, oldB := jiraTokenURL, bbTokenURL
	jiraTokenURL, bbTokenURL = srv.URL, srv.URL
	t.Cleanup(func() { jiraTokenURL, bbTokenURL = oldJ, oldB })
	return &n
}

func connSeedApps(t *testing.T, f *mcpClientFixture) {
	t.Helper()
	ms, err := f.st.ListMemberships(context.Background(), f.alice.ident.ID)
	if err != nil || len(ms) == 0 {
		t.Fatal(err)
	}
	for _, p := range []string{gitOAuthJira, gitOAuthBitbucket} {
		if err := f.st.PutTenantGitOAuth(context.Background(), store.TenantGitOAuth{
			ID: store.NewID(), TenantID: ms[0].TenantID, Provider: p, ClientID: "cid", SecretEnc: "sec",
			CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *mcpClientFixture) connStart(t *testing.T, p connProvider, m *mcpClientMember, email string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/connections/"+p.name+"/oauth/start", nil)
	if m != nil {
		r = f.asMember(r, m)
	} else if email != "" {
		r.Header.Set(f.cfg.mgr.emailHeader, email)
	}
	r.RemoteAddr = "203.0.113.9:4444"
	w := httptest.NewRecorder()
	withClientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.start(f.cfg, w, r) })).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s start: %d %s", p.name, w.Code, w.Body.String())
	}
	var out struct {
		URL string `json:"authorize_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(out.URL)
	return u.Query().Get("state")
}

func (f *mcpClientFixture) connCallback(p connProvider, session *http.Cookie, email, state string) *httptest.ResponseRecorder {
	return f.connCallbackFrom(p, session, email, state, "203.0.113.9:4444")
}

// connCallbackFrom sends the redirect through withClientIP and, under AUTH=oauth, authGate,
// to the real callback path.
func (f *mcpClientFixture) connCallbackFrom(p connProvider, session *http.Cookie, email, state, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/oauth/"+p.name+"/callback?code=c&state="+state, nil)
	r.RemoteAddr = remote
	r.Header.Set("Accept-Language", "en")
	r.Header.Set("Accept", "text/html")
	if session != nil {
		r.AddCookie(session)
	}
	if email != "" {
		r.Header.Set(f.cfg.mgr.emailHeader, email)
	}
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.callback(f.cfg, w, r) })
	if f.cfg.mgr.authMode == "oauth" {
		h = f.cfg.authGate(h)
	}
	w := httptest.NewRecorder()
	withClientIP(h).ServeHTTP(w, r)
	return w
}

func saves(m *mcpClientMember, path string) int {
	n := 0
	for _, c := range m.agent.snapshot() {
		if c.method == http.MethodPut && c.path == path {
			n++
		}
	}
	return n
}

func TestConnectionCallbackAuthOAuth(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			connSeedApps(t, f)
			exchanges := connTokenServer(t)

			// Another member's session, no session: nothing exchanged, nothing stored.
			state := f.connStart(t, p, f.alice, "")
			for name, sess := range map[string]*http.Cookie{"bob": f.bob.session, "none": nil} {
				st := state
				if name == "none" {
					st = f.connStart(t, p, f.alice, "")
				}
				w := f.connCallback(p, sess, "", st)
				if name == "none" {
					// authGate answers first: sign in, then the redirect resumes.
					if loc := w.Header().Get("Location"); w.Code != http.StatusFound || !strings.HasPrefix(loc, "/login") {
						t.Errorf("none: status %d Location %q", w.Code, loc)
					}
				} else if !strings.Contains(w.Body.String(), "Nothing was saved") {
					t.Errorf("%s: expected the failure page, got %s", name, w.Body.String())
				}
				if exchanges.Load() != 0 || saves(f.alice, p.savePath)+saves(f.bob, p.savePath) != 0 {
					t.Fatalf("%s: tokens were exchanged or stored", name)
				}
			}
			// The refused state is spent.
			if w := f.connCallback(p, f.alice.session, "", state); !strings.Contains(w.Body.String(), "state mismatch") {
				t.Errorf("a refused state stayed usable: %s", w.Body.String())
			}

			// The member who started it connects, into their own workspace.
			state = f.connStart(t, p, f.alice, "")
			w := f.connCallback(p, f.alice.session, "", state)
			if !strings.Contains(w.Body.String(), "connection complete") {
				t.Fatalf("normal flow failed: %s", w.Body.String())
			}
			if saves(f.alice, p.savePath) != 1 || saves(f.bob, p.savePath) != 0 || exchanges.Load() != 1 {
				t.Errorf("alice=%d bob=%d exchanges=%d", saves(f.alice, p.savePath), saves(f.bob, p.savePath), exchanges.Load())
			}
		})
	}
}

// The destination comes from the recorded membership: once it is gone the callback stops
// before exchanging the code.
func TestConnectionCallbackInactiveMembershipFails(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			connSeedApps(t, f)
			exchanges := connTokenServer(t)
			state := f.connStart(t, p, f.alice, "")
			p.flows.mu.Lock()
			st := p.flows.states[state]
			st.membershipID = "gone"
			p.flows.states[state] = st
			p.flows.mu.Unlock()
			w := f.connCallback(p, f.alice.session, "", state)
			if strings.Contains(w.Body.String(), "connection complete") || exchanges.Load() != 0 || saves(f.alice, p.savePath) != 0 {
				t.Errorf("stored for a membership that is not active: %s", w.Body.String())
			}
		})
	}
}

func TestConnectionCallbackAuthProxy(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			f.cfg.mgr.authMode = "proxy"
			connSeedApps(t, f)
			exchanges := connTokenServer(t)
			for _, email := range []string{f.bob.email, ""} {
				state := f.connStart(t, p, nil, f.alice.email)
				w := f.connCallback(p, nil, email, state)
				if !strings.Contains(w.Body.String(), "Nothing was saved") {
					t.Errorf("%q: %s", email, w.Body.String())
				}
			}
			if exchanges.Load() != 0 {
				t.Fatal("exchanged for another member")
			}
			state := f.connStart(t, p, nil, f.alice.email)
			w := f.connCallback(p, nil, f.alice.email, state)
			if !strings.Contains(w.Body.String(), "connection complete") || saves(f.alice, p.savePath) != 1 {
				t.Errorf("normal flow: %s", w.Body.String())
			}
		})
	}
}

func TestConnectionCallbackAuthDev(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			connSeedApps(t, f)
			f.cfg.mgr.authMode = "dev"
			f.cfg.mgr.devUser = "dev"
			connTokenServer(t)
			// The one fixed user is both starter and caller; give it a workspace.
			ident, err := f.cfg.mgr.upsertIdentity(context.Background(), "", "dev", "")
			if err != nil {
				t.Fatal(err)
			}
			ms, _ := f.st.ListMemberships(context.Background(), f.alice.ident.ID)
			mem, err := f.st.EnsureMembership(context.Background(), ident.ID, ms[0].TenantID, "member")
			if err != nil {
				t.Fatal(err)
			}
			ws := store.Workspace{ID: "W-dev", TenantID: ms[0].TenantID, MembershipID: mem.ID, ContainerName: "af-ws-dev",
				DataDir: "/srv/dev", AgentPort: "7731", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
			if err := f.st.CreateWorkspace(context.Background(), ws); err != nil {
				t.Fatal(err)
			}
			f.cfg.mgr.rts[mem.ID] = cachedRT{rt: stubRuntime{endpoint: f.alice.agent.srv.URL, token: "tok"}, ws: ws}
			state := f.connStart(t, p, nil, "")
			w := f.connCallback(p, nil, "", state)
			if !strings.Contains(w.Body.String(), "connection complete") || saves(f.alice, p.savePath) != 1 {
				t.Errorf("dev flow: %s", w.Body.String())
			}
		})
	}
}

func TestOAuthCallbackTextHasSessionMismatch(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		if oauthCallbackText(lang, "Jira").sessionMismatch == "" {
			t.Errorf("%s: sessionMismatch is empty", lang)
		}
	}
}

// A session-less callback stops at authGate with the state untouched, so the member can sign
// in and finish: the login redirect resumes the same URL.
func TestConnectionCallbackWithoutSessionKeepsTheState(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			connSeedApps(t, f)
			exchanges := connTokenServer(t)
			state := f.connStart(t, p, f.alice, "")
			if w := f.connCallback(p, nil, "", state); w.Code != http.StatusFound {
				t.Fatalf("status %d", w.Code)
			}
			if exchanges.Load() != 0 || saves(f.alice, p.savePath) != 0 {
				t.Fatal("stored without a session")
			}
			w := f.connCallback(p, f.alice.session, "", state)
			if !strings.Contains(w.Body.String(), "connection complete") {
				t.Errorf("resumed flow: %s", w.Body.String())
			}
		})
	}
}

// The recorded membership's tenant rules are checked against the session and address that
// reach the callback, not the ones at start: a rule changed in between, or a different
// sign-in method or network, is refused before anything is exchanged.
func TestConnectionCallbackAppliesTheTenantRules(t *testing.T) {
	for _, p := range connProviders {
		t.Run(p.name, func(t *testing.T) {
			f := newMCPClientFixture(t)
			connSeedApps(t, f)
			exchanges := connTokenServer(t)
			ctx := context.Background()
			ms, _ := f.st.ListMemberships(ctx, f.alice.ident.ID)
			tid := ms[0].TenantID

			// Network rule.
			if err := f.st.SetTenantAllowedCIDRs(ctx, tid, "203.0.113.0/24"); err != nil {
				t.Fatal(err)
			}
			f.cfg.mgr.tenantLogin.invalidate()
			state := f.connStart(t, p, f.alice, "")
			w := f.connCallbackFrom(p, f.alice.session, "", state, "198.51.100.7:4444")
			if strings.Contains(w.Body.String(), "connection complete") || exchanges.Load() != 0 || saves(f.alice, p.savePath) != 0 {
				t.Fatalf("outside the network rule: %s", w.Body.String())
			}
			state = f.connStart(t, p, f.alice, "")
			w = f.connCallbackFrom(p, f.alice.session, "", state, "203.0.113.9:4444")
			if !strings.Contains(w.Body.String(), "connection complete") {
				t.Fatalf("inside the network rule: %s", w.Body.String())
			}
			if err := f.st.SetTenantAllowedCIDRs(ctx, tid, ""); err != nil {
				t.Fatal(err)
			}

			// Sign-in method rule, changed after the flow started.
			before := exchanges.Load()
			state = f.connStart(t, p, f.alice, "")
			if err := f.st.SetTenantLogin(ctx, tid, "some-other-idp", "", "", ""); err != nil {
				t.Fatal(err)
			}
			f.cfg.mgr.tenantLogin.invalidate()
			w = f.connCallback(p, f.alice.session, "", state)
			if strings.Contains(w.Body.String(), "connection complete") || exchanges.Load() != before {
				t.Fatalf("wrong sign-in method: %s", w.Body.String())
			}
			if err := f.st.SetTenantLogin(ctx, tid, "google", "", "", ""); err != nil {
				t.Fatal(err)
			}
			f.cfg.mgr.tenantLogin.invalidate()
			state = f.connStart(t, p, f.alice, "")
			w = f.connCallback(p, f.alice.session, "", state)
			if !strings.Contains(w.Body.String(), "connection complete") {
				t.Fatalf("allowed sign-in method: %s", w.Body.String())
			}
		})
	}
}
