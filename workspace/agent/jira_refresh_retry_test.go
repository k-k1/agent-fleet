package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// bridgeStub answers the CP refresh route by calling step with the 1-based call number and
// the refresh token received; it records every token it was sent.
type bridgeStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
	done chan struct{}
}

func newBridgeStub(t *testing.T, step func(n int, w http.ResponseWriter, r *http.Request)) *bridgeStub {
	t.Helper()
	b := &bridgeStub{done: make(chan struct{})}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.seen = append(b.seen, body.RefreshToken)
		n := len(b.seen)
		b.mu.Unlock()
		step(n, w, r)
	}))
	t.Cleanup(func() { close(b.done); b.srv.Close() })
	return b
}

func (b *bridgeStub) tokens() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

// hang holds the request until the client gives up or the test ends.
func (b *bridgeStub) hang(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-b.done:
	}
}

func okGrant(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"access_token":"at1","refresh_token":"rt1","expires_in":3600}`))
}

func setupJiraRefresh(t *testing.T, b *bridgeStub) *secrets.JiraCreds {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.GitOAuthBridge = &secrets.CPBridge{BaseURL: b.srv.URL, Token: "afo_x"}
	s.Jira = &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "old", RefreshToken: "rt0", Expiry: 1,
		CloudID: "cid", Site: "https://x.atlassian.net"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	oldA, oldP, oldB := jiraRefreshAttemptTimeout, jiraRefreshRetryPause, jiraRefreshBudget
	t.Cleanup(func() { jiraRefreshAttemptTimeout, jiraRefreshRetryPause, jiraRefreshBudget = oldA, oldP, oldB })
	jiraRefreshAttemptTimeout, jiraRefreshRetryPause = 150*time.Millisecond, 10*time.Millisecond
	c := *s.Jira
	return &c
}

// A transport failure after the CP may already have rotated the token is retried with the
// SAME stored refresh token, and the pair the retry returns is what gets persisted.
func TestJiraRefreshRetriesTransportFailure(t *testing.T) {
	cases := map[string]func(b *bridgeStub, w http.ResponseWriter, r *http.Request){
		"timeout": func(b *bridgeStub, w http.ResponseWriter, r *http.Request) { b.hang(r) },
		"reset": func(b *bridgeStub, w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		},
		"cp 503": func(b *bridgeStub, w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"jira_unreachable"}`, http.StatusServiceUnavailable)
		},
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			var b *bridgeStub
			b = newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) {
				if n == 1 {
					fail(b, w, r)
					return
				}
				okGrant(w)
			})
			c := setupJiraRefresh(t, b)
			if err := jiraRefreshNow(c); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if got := b.tokens(); len(got) != 2 || got[0] != "rt0" || got[1] != "rt0" {
				t.Fatalf("tokens sent = %v, want rt0 twice", got)
			}
			after, _ := secrets.Load()
			if after.Jira.RefreshToken != "rt1" || after.Jira.AccessToken != "at1" {
				t.Errorf("new pair not persisted: %+v", after.Jira)
			}
			if c.RefreshToken != "rt1" || c.AccessToken != "at1" {
				t.Errorf("caller copy not updated: %+v", c)
			}
		})
	}
}

// invalid_grant is final: retrying cannot help and the member has to be told at once.
func TestJiraRefreshDoesNotRetryInvalidGrant(t *testing.T) {
	b := newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	})
	c := setupJiraRefresh(t, b)
	if err := jiraRefreshNow(c); err == nil {
		t.Fatal("invalid_grant was swallowed")
	}
	if got := b.tokens(); len(got) != 1 {
		t.Fatalf("bridge called %d times (%v), want 1", len(got), got)
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "rt0" {
		t.Errorf("stored refresh token changed: %+v", after.Jira)
	}
}

// A bridge that never answers must still return inside jiraRefreshBudget, however the
// attempts are sized.
func TestJiraRefreshStaysInsideBudget(t *testing.T) {
	var b *bridgeStub
	b = newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) { b.hang(r) })
	c := setupJiraRefresh(t, b)
	jiraRefreshAttemptTimeout = time.Minute // only the budget can cut the call
	jiraRefreshBudget = 400 * time.Millisecond
	start := time.Now()
	err := jiraRefreshNow(c)
	if err == nil {
		t.Fatal("hung bridge reported success")
	}
	if el := time.Since(start); el > jiraRefreshBudget+time.Second {
		t.Fatalf("took %v, budget %v", el, jiraRefreshBudget)
	}
	if got := b.tokens(); len(got) == 0 || got[0] != "rt0" {
		t.Errorf("tokens sent = %v", got)
	}
}

// A stored pair replaced while the grant was in flight is not overwritten by the older grant.
func TestJiraRefreshDoesNotOverwriteNewerStoredPair(t *testing.T) {
	var b *bridgeStub
	b = newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) {
		s, _ := secrets.Load()
		s.Jira.AccessToken, s.Jira.RefreshToken, s.Jira.Expiry = "atNew", "rtNew", time.Now().Add(time.Hour).Unix()
		_ = s.Save()
		okGrant(w)
	})
	c := setupJiraRefresh(t, b)
	if err := jiraRefreshNow(c); err != nil {
		t.Fatal(err)
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "rtNew" || c.RefreshToken != "rtNew" {
		t.Errorf("older grant overwrote the newer pair: store=%+v caller=%+v", after.Jira, c)
	}
}

// The retry count is bounded: a bridge that keeps answering 503 is called exactly
// jiraRefreshMaxAttempts times, and the last error is returned.
func TestJiraRefreshAttemptsAreBounded(t *testing.T) {
	b := newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"jira_unreachable"}`, http.StatusServiceUnavailable)
	})
	c := setupJiraRefresh(t, b)
	if err := jiraRefreshNow(c); err == nil {
		t.Fatal("persistent 503 reported success")
	}
	if got := b.tokens(); len(got) != jiraRefreshMaxAttempts {
		t.Fatalf("bridge called %d times, want %d", len(got), jiraRefreshMaxAttempts)
	}
}

// A 200 with an unusable body is retried like a cut body: the CP most likely completed the
// exchange, so the same token must be presented again.
func TestJiraRefreshRetriesMalformed200(t *testing.T) {
	for name, bad := range map[string]string{"bad json": `{"access_tok`, "no access_token": `{"refresh_token":"x"}`} {
		t.Run(name, func(t *testing.T) {
			b := newBridgeStub(t, func(n int, w http.ResponseWriter, r *http.Request) {
				if n == 1 {
					_, _ = w.Write([]byte(bad))
					return
				}
				okGrant(w)
			})
			c := setupJiraRefresh(t, b)
			if err := jiraRefreshNow(c); err != nil {
				t.Fatal(err)
			}
			if got := b.tokens(); len(got) != 2 || got[1] != "rt0" {
				t.Fatalf("tokens = %v", got)
			}
		})
	}
}
