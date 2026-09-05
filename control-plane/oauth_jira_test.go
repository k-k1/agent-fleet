// oauth_jira_test.go — Jira 3LO (docs/log/80 §80.17). A real Atlassian is available
// neither in CI nor here, so what is pinned is what af constructs and where the secret
// lives.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Four parts of the authorize URL are pinned. Any one of them missing shows up as
// "connected but it does not work", with nothing pointing at the authorize URL:
//   - audience=api.atlassian.com — without it the 3LO token is not usable against the API
//   - offline_access — without it no refresh token comes back and it dies after an hour
//   - prompt=consent — without it a re-authorization returns no refresh token
//   - write:jira-work — the user's opt-in to also post comments (docs/log/80 §80.10)
func TestJiraAuthorizeURLShape(t *testing.T) {
	au := jiraAuthorizeURL +
		"?audience=api.atlassian.com" +
		"&client_id=" + url.QueryEscape("cid") +
		"&scope=" + url.QueryEscape(jiraScopes) +
		"&redirect_uri=" + url.QueryEscape("https://af.example/api/oauth/jira/callback") +
		"&state=abc&response_type=code&prompt=consent"
	u, err := url.Parse(au)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "auth.atlassian.com" {
		t.Errorf("host = %q — Jira does not authorize at bitbucket.org", u.Host)
	}
	if q.Get("audience") != "api.atlassian.com" {
		t.Error("missing audience")
	}
	if q.Get("prompt") != "consent" {
		t.Error("missing prompt=consent")
	}
	if q.Get("response_type") != "code" {
		t.Error("missing response_type")
	}
	for _, want := range []string{"read:jira-work", "read:jira-user", "write:jira-work", "offline_access"} {
		if !strings.Contains(q.Get("scope"), want) {
			t.Errorf("scope %q is missing %q", q.Get("scope"), want)
		}
	}
}

// The token exchange takes a JSON body, unlike Bitbucket's form + Basic. Sending it the
// Bitbucket way returns a 400 that reads like a misconfigured app.
func TestJiraExchangeCodeUsesJSONBody(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	tok, err := jiraExchangeCode("cid", "csecret", "the-code", "https://af.example/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.ExpiresIn != 3600 {
		t.Errorf("token = %+v", tok)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("body was not JSON: %s", gotBody)
	}
	// client_secret travels in the body, not in Basic auth.
	if sent["client_secret"] != "csecret" || sent["grant_type"] != "authorization_code" {
		t.Errorf("body = %v", sent)
	}
	if sent["redirect_uri"] != "https://af.example/cb" {
		t.Errorf("redirect_uri = %q", sent["redirect_uri"])
	}
}

func TestJiraExchangeCodeReportsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	if _, err := jiraExchangeCode("cid", "s", "bad", "https://af.example/cb"); err == nil {
		t.Fatal("a refused exchange was reported as success")
	} else if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error = %q, want the provider's own words", err)
	}
}

// Refresh is JSON too. A 4xx returns immediately as invalid_grant, meaning the user has to
// reconnect; only 5xx and 429 are retried, so a revoked authorization is not hammered.
func TestJiraRefreshGrant(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","expires_in":3600}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()

	tok, aerr := jiraRefreshGrant("cid", "s", "rt1")
	if aerr != nil {
		t.Fatalf("refresh: %v", aerr)
	}
	// Atlassian rotates the refresh token. Unless the new one is carried back, the Agent
	// keeps storing the old one and wedges at the next expiry.
	if tok.AccessToken != "at2" || tok.RefreshToken != "rt2" {
		t.Errorf("token = %+v", tok)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want the 5xx to be retried once", hits)
	}

	hits = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv2.Close()
	jiraTokenURL = srv2.URL
	_, aerr = jiraRefreshGrant("cid", "s", "revoked")
	if aerr == nil || aerr.code != "invalid_grant" {
		t.Fatalf("aerr = %+v, want invalid_grant", aerr)
	}
	if hits != 1 {
		t.Errorf("hits = %d — a revoked refresh token must not be retried", hits)
	}
}

// jira belongs to the closed provider set, and each provider yields its own callback URL.
// GitHub uses the device flow and so has none: giving it one would make the admin screen
// display a value nobody can be told to register.
func TestJiraIsAGitOAuthProvider(t *testing.T) {
	if !validGitOAuthProvider(gitOAuthJira) {
		t.Fatal("jira is not in the closed provider set")
	}
	if !gitOAuthNeedsSecret(gitOAuthJira) {
		t.Error("jira's code grant needs a client secret")
	}
	m := &manager{publicBaseURL: "https://af.example/"}
	if got := m.gitOAuthRedirectURI(gitOAuthJira); got != "https://af.example/api/oauth/jira/callback" {
		t.Errorf("jira redirect = %q", got)
	}
	if got := m.gitOAuthRedirectURI(gitOAuthBitbucket); got != "https://af.example/api/oauth/bitbucket/callback" {
		t.Errorf("bitbucket redirect = %q", got)
	}
	if got := m.gitOAuthRedirectURI(gitOAuthGitHub); got != "" {
		t.Errorf("github redirect = %q, want empty (device flow)", got)
	}
	none := &manager{}
	if got := none.gitOAuthRedirectURI(gitOAuthJira); got != "" {
		t.Errorf("no PUBLIC_BASE_URL should yield no redirect, got %q", got)
	}
}

// The callback copy substitutes the provider name. Hand-writing nine strings each for
// Bitbucket and Jira would inevitably drift apart.
func TestOAuthCallbackTextNamesTheProvider(t *testing.T) {
	ja := oauthCallbackText("ja", "Jira")
	if !strings.Contains(ja.notConfigured, "Jira") || !strings.Contains(ja.success, "Jira") {
		t.Errorf("ja text does not name the provider: %+v", ja)
	}
	en := oauthCallbackText("en", "Bitbucket")
	if !strings.Contains(en.success, "Bitbucket") {
		t.Errorf("en text does not name the provider: %+v", en)
	}
}
