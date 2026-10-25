package gitx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// #2709 — recording the scopes Bitbucket granted. All stubs; no real Bitbucket.

// A refresh that names scopes updates them; one that names none keeps what was recorded.
func TestBitbucketRefreshViaCPRecordsScopes(t *testing.T) {
	withAgentHome(t)
	answer := `{"access_token":"fresh","expires_in":3600,"scopes":"account pullrequest:write"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	b := secrets.CPBridge{BaseURL: srv.URL, Token: "afo_tok"}
	nc, err := refreshBitbucketViaCP(b, secrets.BitbucketCreds{AccessToken: "s", RefreshToken: "r"})
	if err != nil || nc.Scopes != "account pullrequest:write" {
		t.Fatalf("scopes = %q err=%v", nc.Scopes, err)
	}
	answer = `{"access_token":"fresher","expires_in":3600}`
	nc, err = refreshBitbucketViaCP(b, nc)
	if err != nil || nc.Scopes != "account pullrequest:write" {
		t.Fatalf("a refresh naming no scopes must keep them: %q err=%v", nc.Scopes, err)
	}
}

func TestBitbucketStoreRecordsScopes(t *testing.T) {
	withAgentHome(t)
	// The handler also installs the credential helper, which the gitx wiring never reaches.
	d := testDeps()
	d.EnsureCredHelper = func() error { return nil }
	Configure(d)
	defer Configure(testDeps())
	w := httptest.NewRecorder()
	HandleBitbucketStore(w, httptest.NewRequest("PUT", "/connections/git/bitbucket/oauth",
		strings.NewReader(`{"access_token":"a","refresh_token":"r","expires_in":60,"scopes":" account pullrequest:write "}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("store: %d %s", w.Code, w.Body.String())
	}
	s, err := secrets.Load()
	if err != nil || s.Bitbucket == nil || s.Bitbucket.Scopes != "account pullrequest:write" {
		t.Fatalf("stored: %+v err=%v", s.Bitbucket, err)
	}
	// An older CP sends no scopes: the connection stores fine and the scopes stay unknown.
	w = httptest.NewRecorder()
	HandleBitbucketStore(w, httptest.NewRequest("PUT", "/connections/git/bitbucket/oauth",
		strings.NewReader(`{"access_token":"a","refresh_token":"r","expires_in":60}`)))
	s, _ = secrets.Load()
	if w.Code != http.StatusOK || s.Bitbucket.Scopes != "" {
		t.Fatalf("no-scopes store: %d %+v", w.Code, s.Bitbucket)
	}
}

func TestBitbucketConnectCheckReturnsScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "write:pullrequest:bitbucket, read:repository:bitbucket")
	}))
	defer srv.Close()
	_, scopes, err := bitbucketConnectCheckAt(srv.URL, "me@example.com", "tok")
	if err != nil || scopes != "read:repository:bitbucket write:pullrequest:bitbucket" {
		t.Fatalf("scopes = %q err=%v", scopes, err)
	}
	if !BitbucketScopesCanComment(scopes) {
		t.Fatal("granular write scope must allow commenting")
	}
}

func TestBitbucketScopesCanComment(t *testing.T) {
	for list, want := range map[string]bool{
		"":                           false,
		"pullrequest":                false, // read does not imply write
		"read:pullrequest:bitbucket": false,
		"repository:write account":   false,
		"pullrequest:write":          true,
		"PullRequest:Write":          true,
		"read:pullrequest:bitbucket write:pullrequest:bitbucket": true,
	} {
		if got := BitbucketScopesCanComment(list); got != want {
			t.Errorf("%q = %v, want %v", list, got, want)
		}
	}
}
