package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The pair the device flow grants reaches the member's Agent whole: the refresh token and
// its lifetimes next to the access token, plus the app's client id — and nothing else. In
// particular there is no client_secret to send: GitHub's refresh grant for a device-flow
// token takes none, and the CP never holds one for this flow.
func TestPutGithubTokenSendsTheRefreshPairAndClientID(t *testing.T) {
	var got map[string]any
	var auth, method, path string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"connected":true,"renewable":true}`))
	}))
	defer agent.Close()

	renewable, aerr := putGithubToken(agent.URL, "fake-agent-token", "fake-client-id", ghTokenPair{
		Access: "fake-access", Refresh: "fake-refresh", ExpiresIn: 28800, RefreshExpiresIn: 15811200,
	})
	if aerr != nil || !renewable {
		t.Fatalf("renewable=%v err=%v", renewable, aerr)
	}
	if method != "PUT" || path != "/connections/git/github.com" || auth != "Bearer fake-agent-token" {
		t.Fatalf("request: %s %s auth=%q", method, path, auth)
	}
	want := map[string]any{
		"token": "fake-access", "refresh_token": "fake-refresh", "client_id": "fake-client-id",
		"expires_in": float64(28800), "refresh_token_expires_in": float64(15811200),
	}
	if len(got) != len(want) {
		t.Fatalf("body fields: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("body[%s] = %v, want %v", k, got[k], v)
		}
	}
}

// A token with no refresh token (a PAT-like OAuth App token, or an app that does not expire
// its tokens) is sent exactly as before.
func TestPutGithubTokenWithoutARefreshTokenIsUnchanged(t *testing.T) {
	var got map[string]any
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"connected":true}`))
	}))
	defer agent.Close()
	renewable, aerr := putGithubToken(agent.URL, "", "fake-client-id", ghTokenPair{Access: "fake-access"})
	if aerr != nil || renewable {
		t.Fatalf("renewable=%v err=%v", renewable, aerr)
	}
	if len(got) != 1 || got["token"] != "fake-access" {
		t.Fatalf("body: %v", got)
	}
}

// An Agent from before renewal stores the access token and ignores the rest; its answer
// has no "renewable", which is what keeps the Console's "this will expire" warning alive.
func TestPutGithubTokenToAnOlderAgentIsNotRenewable(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true,"host":"github.com","username":"x-access-token"}`))
	}))
	defer agent.Close()
	renewable, aerr := putGithubToken(agent.URL, "", "fake-client-id", ghTokenPair{Access: "a", Refresh: "r", ExpiresIn: 1})
	if aerr != nil || renewable {
		t.Fatalf("renewable=%v err=%v", renewable, aerr)
	}
}

func TestPutGithubTokenErrorsCarryNoToken(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "store is read-only", http.StatusInternalServerError)
	}))
	defer agent.Close()
	_, aerr := putGithubToken(agent.URL, "", "fake-client-id", ghTokenPair{Access: "fake-access", Refresh: "fake-refresh"})
	if aerr == nil || strings.Contains(aerr.message, "fake-access") || strings.Contains(aerr.message, "fake-refresh") {
		t.Fatalf("err=%+v", aerr)
	}
}
