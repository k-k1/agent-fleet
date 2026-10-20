package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func seedJiraStore(t *testing.T, fn func(s *secrets.Data)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error { fn(s); return nil }); err != nil {
		t.Fatal(err)
	}
}

func jiraCredOut() string {
	var out bytes.Buffer
	jiraCredGet(&out)
	return out.String()
}

func TestJiraCredServesAnAPITokenConnectionAsBasic(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: "https://acme.atlassian.net", Email: "me@example.com", Token: "fake-api-token"}
	})
	want := "site=https://acme.atlassian.net\nserver=https://acme.atlassian.net\nauth=basic\n" +
		"login=me@example.com\ntoken=fake-api-token\n"
	if got := jiraCredOut(); got != want {
		t.Fatalf("answered %q, want %q", got, want)
	}
}

// A 3LO token is refused by the site's own host, so server must be the api.atlassian.com
// route while site stays the human-facing base.
func TestJiraCredServesAFreshOAuthTokenAsBearerOnTheCloudRoute(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			Login: "me@example.com", AccessToken: "fake-at", RefreshToken: "fake-rt",
			Expiry: time.Now().Add(time.Hour).Unix()}
	})
	want := "site=https://acme.atlassian.net\nserver=https://api.atlassian.com/ex/jira/cid-1\nauth=bearer\n" +
		"login=me@example.com\ntoken=fake-at\n"
	if got := jiraCredOut(); got != want {
		t.Fatalf("answered %q, want %q", got, want)
	}
}

// software=missing only for an OAuth site whose recorded scopes lack the board scope; granted,
// unrecorded (old store) and API-token connections stay silent.
func TestJiraCredFlagsMissingSoftwareScopes(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		scopes []string
		want   bool
	}{
		{"missing", "oauth", []string{"read:jira-work", "offline_access"}, true},
		{"granted", "oauth", []string{"read:jira-work", jiraBoardScope}, false},
		{"unrecorded", "oauth", nil, false},
		{"api token", "", []string{"read:jira-work"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The unrecorded case would otherwise try the backfill against the real Atlassian.
			stubAccessibleResources(t, `[]`, 500)
			seedJiraStore(t, func(s *secrets.Data) {
				s.Jira = &secrets.JiraCreds{AuthKind: c.kind, Site: "https://acme.atlassian.net", CloudID: "cid-1",
					AccessToken: "fake-at", Token: "fake-api", Email: "me@example.com", Expiry: time.Now().Add(time.Hour).Unix(),
					Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net", Scopes: c.scopes}}}
			})
			got := jiraCredOut()
			if has := strings.Contains(got, "software=missing\n"); has != c.want {
				t.Fatalf("answered %q, software=missing want %v", got, c.want)
			}
			if c.kind == "oauth" && !strings.HasSuffix(got, "token=fake-at\n") {
				t.Fatalf("token is not last: %q", got)
			}
		})
	}
}

func TestJiraCredOmitsAnUnknownOAuthLogin(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix()}
	})
	got := jiraCredOut()
	if strings.Contains(got, "login=") || !strings.HasSuffix(got, "token=fake-at\n") {
		t.Fatalf("answered %q", got)
	}
}

// Within jiraCredFreshWindow the token is renewed before it is handed out, and the rotated
// pair is what the store keeps.
func TestJiraCredRenewsATokenAboutToExpire(t *testing.T) {
	var grants []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		grants = append(grants, body.RefreshToken)
		_, _ = w.Write([]byte(`{"access_token":"fake-at2","refresh_token":"fake-rt2","expires_in":3600}`))
	}))
	defer srv.Close()
	seedJiraStore(t, func(s *secrets.Data) {
		s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at1", RefreshToken: "fake-rt1", Expiry: time.Now().Add(2 * time.Minute).Unix()}
	})
	if got := jiraCredOut(); !strings.HasSuffix(got, "token=fake-at2\n") {
		t.Fatalf("answered %q, want the renewed token", got)
	}
	if len(grants) != 1 || grants[0] != "fake-rt1" {
		t.Fatalf("grants = %v", grants)
	}
	after, _ := secrets.Load()
	if after.Jira.AccessToken != "fake-at2" || after.Jira.RefreshToken != "fake-rt2" {
		t.Errorf("rotation not persisted: %+v", after.Jira)
	}
}

// A renewal that fails still serves a token with time left, and serves nothing once it is
// dead: jira then runs without injected auth instead of with a token Jira will refuse.
func TestJiraCredOnAFailedRenewal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	for name, tc := range map[string]struct {
		expiry time.Duration
		want   string
	}{
		"still valid": {2 * time.Minute, "token=fake-at1\n"},
		"expired":     {-time.Minute, ""},
	} {
		t.Run(name, func(t *testing.T) {
			seedJiraStore(t, func(s *secrets.Data) {
				s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
				s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
					AccessToken: "fake-at1", RefreshToken: "fake-rt1", Expiry: time.Now().Add(tc.expiry).Unix()}
			})
			got := jiraCredOut()
			if !strings.HasSuffix(got, tc.want) || (tc.want == "" && got != "") {
				t.Fatalf("answered %q, want suffix %q", got, tc.want)
			}
		})
	}
}

func TestJiraCredServesNothingWithoutAUsableConnection(t *testing.T) {
	cases := map[string]func(s *secrets.Data){
		"no connection": func(s *secrets.Data) {},
		"token without value": func(s *secrets.Data) {
			s.Jira = &secrets.JiraCreds{Site: "https://a.atlassian.net", Email: "me@example.com"}
		},
		"oauth without a site": func(s *secrets.Data) {
			s.Jira = &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix()}
		},
		// A newline would let a stored value forge the lines after it.
		"newline in a field": func(s *secrets.Data) {
			s.Jira = &secrets.JiraCreds{Site: "https://a.atlassian.net", Email: "me@example.com\nauth=bearer", Token: "fake"}
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			seedJiraStore(t, fn)
			if got := jiraCredOut(); got != "" {
				t.Fatalf("answered %q, want nothing", got)
			}
		})
	}
}

// TestJiraCredRaceHelper is the child half of the cross-process test below: a jira-cred run
// in a process of its own, as the wrapper starts it next to the Agent.
func TestJiraCredRaceHelper(t *testing.T) {
	if os.Getenv("AF_JIRA_CRED_RACE") == "" {
		t.Skip("helper process only")
	}
	<-time.After(time.Until(time.Unix(0, mustInt64(os.Getenv("AF_JIRA_CRED_RACE")))))
	jiraCredGet(os.Stdout)
}

func mustInt64(s string) int64 {
	var n int64
	_, _ = fmt.Sscan(s, &n)
	return n
}

// The Agent and a jira-cred process both notice the expiry at the same moment. The rotated
// refresh token may be presented to Atlassian once only, so exactly one grant may run, and
// both sides must end up holding the token that grant produced.
func TestJiraRefreshIsSerializedAcrossProcesses(t *testing.T) {
	var mu sync.Mutex
	var grants []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		grants = append(grants, body.RefreshToken)
		n := len(grants)
		mu.Unlock()
		time.Sleep(100 * time.Millisecond) // long enough for the other side to arrive
		_, _ = fmt.Fprintf(w, `{"access_token":"fake-at%d","refresh_token":"fake-rt%d","expires_in":3600}`, n, n)
	}))
	defer srv.Close()
	seedJiraStore(t, func(s *secrets.Data) {
		s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at0", RefreshToken: "fake-rt0", Expiry: 1}
	})

	start := time.Now().Add(300 * time.Millisecond)
	cmd := exec.Command(os.Args[0], "-test.run=^TestJiraCredRaceHelper$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("AF_JIRA_CRED_RACE=%d", start.UnixNano()))
	var child bytes.Buffer
	cmd.Stdout = &child
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(start))
	cur, _ := secrets.Load()
	c := *cur.Jira
	refreshErr := jiraEnsureFresh(&c)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v\n%s", err, child.String())
	}
	if refreshErr != nil {
		t.Fatal(refreshErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(grants) != 1 {
		t.Fatalf("refresh grant ran %d times (%v) — a rotated token must not be presented twice", len(grants), grants)
	}
	if c.AccessToken != "fake-at1" || !strings.Contains(child.String(), "token=fake-at1\n") {
		t.Errorf("agent holds %q, jira-cred served %q; both want fake-at1", c.AccessToken, child.String())
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "fake-rt1" {
		t.Errorf("stored refresh token = %q", after.Jira.RefreshToken)
	}
}

func stubAccessibleResources(t *testing.T, body string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token/accessible-resources" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	old, oldLast := jiraCloudAPIBase, jiraBackfillLast
	jiraCloudAPIBase = srv.URL
	jiraBackfillLast = time.Time{}
	t.Cleanup(func() { srv.Close(); jiraCloudAPIBase = old; jiraBackfillLast = oldLast })
}

func seedLegacyOAuth(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net"}}}
	})
}

// A connection stored before scopes were recorded learns its verdict from accessible-resources
// on the next jira-cred, without reconnecting; the scopes are persisted, and a later reconnect
// that grants the board scope clears the warning.
func TestJiraCredBackfillsScopesOfAnOlderConnection(t *testing.T) {
	seedLegacyOAuth(t)
	stubAccessibleResources(t, `[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`, 200)
	if got := jiraCredOut(); !strings.Contains(got, "software=missing\n") {
		t.Fatalf("answered %q, want software=missing", got)
	}
	s, _ := secrets.Load()
	if len(s.Jira.Sites[0].Scopes) != 1 || jiraStatus(s)["softwareScopes"] != "missing" {
		t.Fatalf("scopes not persisted: %+v", s.Jira.Sites)
	}
	// Reconnect (what handleJiraOAuthStore stores) with the board scope: granted, no warning.
	_ = secrets.Update(func(s *secrets.Data) error {
		s.Jira.Sites[0].Scopes = []string{"read:jira-work", jiraBoardScope}
		return nil
	})
	if got := jiraCredOut(); strings.Contains(got, "software=missing") {
		t.Fatalf("warning stays after reconnect: %q", got)
	}
}

func TestJiraCredBackfillFailureStaysUnknown(t *testing.T) {
	seedLegacyOAuth(t)
	stubAccessibleResources(t, `nope`, 500)
	got := jiraCredOut()
	if strings.Contains(got, "software=") || !strings.HasSuffix(got, "token=fake-at\n") {
		t.Fatalf("answered %q", got)
	}
	s, _ := secrets.Load()
	if len(s.Jira.Sites[0].Scopes) != 0 {
		t.Fatalf("scopes stored after a failed lookup: %+v", s.Jira.Sites)
	}
}

// The rail's own fetches fill the scopes too, so the Settings card learns without a jira run.
func TestJiraGetBackfillsScopes(t *testing.T) {
	seedLegacyOAuth(t)
	stubAccessibleResources(t, `[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`, 200)
	s, _ := secrets.Load()
	c := s.Jira
	_, _ = jiraGet(c, jiraCloudAPIBase+"/oauth/token/accessible-resources")
	s, _ = secrets.Load()
	if jiraStatus(s)["softwareScopes"] != "missing" {
		t.Fatalf("status = %v", jiraStatus(s))
	}
}

// Opening Settings alone (no jira run, no rail fetch) fills in an older connection's scopes.
func TestConnectionsGetBackfillsJiraScopes(t *testing.T) {
	seedLegacyOAuth(t)
	stubAccessibleResources(t, `[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`, 200)
	w := httptest.NewRecorder()
	handleConnectionsGet(w, httptest.NewRequest("GET", "/connections", nil))
	var out struct {
		Jira map[string]any `json:"jira"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Jira["softwareScopes"] != "missing" {
		t.Fatalf("jira status = %v", out.Jira)
	}
	if s, _ := secrets.Load(); len(s.Jira.Sites[0].Scopes) == 0 {
		t.Fatal("scopes not stored")
	}
}
