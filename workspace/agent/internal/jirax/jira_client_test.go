package jirax

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// A 3LO token does not work against the site host. This pins that the API base changes with the
// auth kind: get it wrong and the symptom is a 401, which reads as "the token is wrong".
func TestJiraAPIBaseSwitchesWithAuthKind(t *testing.T) {
	tokenAuth := &secrets.JiraCreds{Site: "https://example.atlassian.net", Email: "a@example.com", Token: "t"}
	if got := APIBase(tokenAuth); got != "https://example.atlassian.net" {
		t.Errorf("token base = %q", got)
	}
	oauth := &secrets.JiraCreds{AuthKind: "oauth", Site: "https://example.atlassian.net", CloudID: "cid-1", AccessToken: "at"}
	if got := APIBase(oauth); got != "https://api.atlassian.com/ex/jira/cid-1" {
		t.Errorf("oauth base = %q", got)
	}
	// The auth header switches too.
	if h := jiraAuthHeader(oauth); h != "Bearer at" {
		t.Errorf("oauth header = %q", h)
	}
	if h := jiraAuthHeader(tokenAuth); !strings.HasPrefix(h, "Basic ") {
		t.Errorf("token header = %q", h)
	}
	// Site stays the URL a human sees: a browse link is not on api.atlassian.com.
	if oauth.Site != "https://example.atlassian.net" {
		t.Errorf("oauth site = %q", oauth.Site)
	}
}

// Deciding "connected" from the Token field alone makes an OAuth connection look disconnected.
func TestJiraConnected(t *testing.T) {
	if Connected(nil) {
		t.Error("nil is connected")
	}
	if Connected(&secrets.JiraCreds{Site: "https://x"}) {
		t.Error("a site alone is not a connection")
	}
	if !Connected(&secrets.JiraCreds{Token: "t"}) {
		t.Error("api token path not recognised")
	}
	if !Connected(&secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at"}) {
		t.Error("oauth path not recognised")
	}
	// An expired access token with a refresh token still in hand is a live connection.
	if !Connected(&secrets.JiraCreds{AuthKind: "oauth", RefreshToken: "rt"}) {
		t.Error("an expired-but-refreshable connection reads as disconnected")
	}
}

// Refresh before the request when expiry is near, and PERSIST the result: Atlassian rotates the
// refresh token, so dropping the save wedges the connection at the next expiry.
func TestJiraEnsureFreshOnlyWhenOAuthAndDue(t *testing.T) {
	// Token auth has no expiry, so nothing happens — not even an error when there is no bridge.
	tokenAuth := &secrets.JiraCreds{Site: "https://x", Token: "t"}
	if err := EnsureFresh(tokenAuth); err != nil {
		t.Errorf("token path tried to refresh: %v", err)
	}
	// An expiry far enough away is left alone.
	future := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", Expiry: time.Now().Add(time.Hour).Unix()}
	if err := EnsureFresh(future); err != nil {
		t.Errorf("fresh token refreshed anyway: %v", err)
	}
	// Once expired it goes looking for the bridge, and errors out when there is none rather than
	// quietly calling with the stale token.
	t.Setenv("HOME", t.TempDir())
	expired := &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "at", RefreshToken: "rt", Expiry: 1}
	if err := EnsureFresh(expired); err == nil {
		t.Error("an expired token was used without a refresh")
	}
}

// A rotating refresh token is single-use. Presenting a spent one again is what Atlassian treats
// as theft, and it can revoke the whole authorization — which the user sees as "Jira disconnected
// itself". This pins that the exchange runs exactly once even when two places notice the expiry
// at the same moment.
func TestJiraRefreshIsSerializedAndNotRepeated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var seen []string // the refresh tokens received, in order
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.RefreshToken)
		n := len(seen)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // make the exchange take long enough to overlap
		_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"at%d","refresh_token":"rt%d","expires_in":3600}`, n, n)))
	}))
	defer srv.Close()

	// Put the bridge in the store: this is the refresh route that goes through the CP.
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
	s.Jira = &secrets.JiraCreds{AuthKind: "oauth", AccessToken: "old", RefreshToken: "rt0", Expiry: 1,
		CloudID: "cid", Site: "https://x.atlassian.net"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// The shape where a rail fetch and a comment post notice the expiry at the same time.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cur, _ := secrets.Load()
			if cur == nil || cur.Jira == nil {
				return
			}
			c := *cur.Jira
			_ = EnsureFresh(&c)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("refresh grant ran %d times (%v) — a rotated token must not be presented twice", len(seen), seen)
	}
	if seen[0] != "rt0" {
		t.Errorf("exchanged %q, want the stored refresh token", seen[0])
	}
	after, _ := secrets.Load()
	if after.Jira.RefreshToken != "rt1" || after.Jira.AccessToken != "at1" {
		t.Errorf("rotation not persisted: %+v", after.Jira)
	}
	if after.Jira.Expiry <= time.Now().Unix() {
		t.Errorf("expiry not moved forward: %d", after.Jira.Expiry)
	}
}
