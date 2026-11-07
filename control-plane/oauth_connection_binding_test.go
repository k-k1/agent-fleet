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

// The Jira and Bitbucket connection callbacks are auth-exempt browser redirects. What is
// pinned: the browser session must be the member who started the flow, the tokens go to the
// workspace of the membership recorded at start, and a refused callback exchanges and stores
// nothing.

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
	w := httptest.NewRecorder()
	p.start(f.cfg, w, r)
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
	r := httptest.NewRequest(http.MethodGet, "/oauth2/"+p.name+"/callback?code=c&state="+state, nil)
	r.Header.Set("Accept-Language", "en")
	if session != nil {
		r.AddCookie(session)
	}
	if email != "" {
		r.Header.Set(f.cfg.mgr.emailHeader, email)
	}
	w := httptest.NewRecorder()
	p.callback(f.cfg, w, r)
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
				if !strings.Contains(w.Body.String(), "Nothing was saved") {
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
