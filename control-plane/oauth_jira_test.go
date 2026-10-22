// oauth_jira_test.go — Jira 3LO (docs/log/80 §80.17). A real Atlassian is available
// neither in CI nor here, so what is pinned is what af constructs and where the secret
// lives.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

	tok, err := jiraExchangeCode(context.Background(), "cid", "csecret", "the-code", "https://af.example/cb")
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

	if _, err := jiraExchangeCode(context.Background(), "cid", "s", "bad", "https://af.example/cb"); err == nil {
		t.Fatal("a refused exchange was reported as success")
	} else if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error = %q, want the provider's own words", err)
	}
}

// Refresh is JSON too. A 4xx returns immediately as invalid_grant, meaning the user has to
// reconnect; only 5xx and 429 are retried, so a revoked authorization is not hammered.
func TestJiraRefreshGrant(t *testing.T) {
	fastJiraRetry(t)
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

	tok, aerr := jiraRefreshGrant(context.Background(), "cid", "s", "rt1")
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
	_, aerr = jiraRefreshGrant(context.Background(), "cid", "s", "revoked")
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

// fastJiraRetry swaps both policies' sleep for a recorder so no test waits real seconds,
// and returns the delays asked for.
func fastJiraRetry(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	sl := func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	idj := func(d time.Duration) time.Duration { return d }
	oldR, oldE := jiraRefreshRetry, jiraExchangeRetry
	jiraRefreshRetry.sleep, jiraRefreshRetry.jitter = sl, idj
	jiraExchangeRetry.sleep, jiraExchangeRetry.jitter = sl, idj
	t.Cleanup(func() { jiraRefreshRetry, jiraExchangeRetry = oldR, oldE })
	return &slept
}

// resetThenServe resets the TCP connection for the first n requests (hijack + close, so
// the client sees "connection reset"/EOF exactly as on the affected deployments), then
// answers with ok. hits counts every request.
func resetThenServe(t *testing.T, n int, ok func(w http.ResponseWriter)) (*int, func()) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits <= n {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if tc, isTCP := conn.(*net.TCPConn); isTCP {
				_ = tc.SetLinger(0) // RST rather than FIN
			}
			conn.Close()
			return
		}
		ok(w)
	}))
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	return &hits, func() { jiraTokenURL = old; srv.Close() }
}

func TestJiraExchangeCodeSucceedsAfterResets(t *testing.T) {
	slept := fastJiraRetry(t)
	hits, done := resetThenServe(t, 2, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	})
	defer done()
	tok, err := jiraExchangeCode(context.Background(), "cid", "s", "c", "https://af.example/cb")
	if err != nil {
		t.Fatalf("exchange after 2 resets: %v", err)
	}
	if tok.AccessToken != "at" || *hits != 3 {
		t.Errorf("tok=%+v hits=%d, want success on the third attempt", tok, *hits)
	}
	if len(*slept) != 2 || (*slept)[1] <= (*slept)[0] {
		t.Errorf("backoff = %v, want two growing delays", *slept)
	}
}

func TestJiraExchangeCodeGivesUpOnPersistentResets(t *testing.T) {
	fastJiraRetry(t)
	hits, done := resetThenServe(t, 1000, nil)
	defer done()
	if _, err := jiraExchangeCode(context.Background(), "cid", "s", "c", "https://af.example/cb"); err == nil {
		t.Fatal("persistent resets reported as success")
	}
	if *hits != jiraExchangeRetry.attempts {
		t.Errorf("hits = %d, want %d", *hits, jiraExchangeRetry.attempts)
	}
}

func TestJiraExchangeCodeDoesNotRetryPermanent4xx(t *testing.T) {
	slept := fastJiraRetry(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()
	_, err := jiraExchangeCode(context.Background(), "cid", "s", "used", "https://af.example/cb")
	if err == nil || !jiraCodeConsumed(err) {
		t.Fatalf("err = %v, want a consumed-code error", err)
	}
	if hits != 1 || len(*slept) != 0 {
		t.Errorf("hits=%d sleeps=%v — a permanent 4xx must return at once", hits, *slept)
	}
}

func TestJiraExchangeCodeRetries429And5xx(t *testing.T) {
	fastJiraRetry(t)
	for _, code := range []int{429, 502} {
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if hits == 1 {
				w.WriteHeader(code)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":60}`))
		}))
		old := jiraTokenURL
		jiraTokenURL = srv.URL
		_, err := jiraExchangeCode(context.Background(), "cid", "s", "c", "https://af.example/cb")
		jiraTokenURL = old
		srv.Close()
		if err != nil || hits != 2 {
			t.Errorf("status %d: err=%v hits=%d, want one retry then success", code, err, hits)
		}
	}
}

func TestJiraRefreshGrantSucceedsAfterResets(t *testing.T) {
	slept := fastJiraRetry(t)
	hits, done := resetThenServe(t, 3, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","expires_in":3600}`))
	})
	defer done()
	tok, aerr := jiraRefreshGrant(context.Background(), "cid", "s", "rt1")
	if aerr != nil {
		t.Fatalf("refresh after 3 resets: %v", aerr)
	}
	if tok.RefreshToken != "rt2" || *hits != 4 || len(*slept) != 3 {
		t.Errorf("tok=%+v hits=%d sleeps=%v", tok, *hits, *slept)
	}
}

// Whatever the policy, the delays plus attempts must stay inside what the Agent waits for
// (20 s on its CP call), and the backoff must reach seconds, since resets come in bursts.
func TestJiraRefreshRetryFitsTheAgentBudget(t *testing.T) {
	p := jiraRefreshRetry
	// Headroom for the handler's own work before the grant starts.
	if p.budget > 15*time.Second {
		t.Errorf("budget %v is not under the Agent's 20 s bridge timeout", p.budget)
	}
	if p.max < 2*time.Second {
		t.Errorf("max backoff %v is too short for bursty resets", p.max)
	}
}

// The budget is enforced for real: a hung endpoint with a tiny budget returns promptly
// instead of running attempts x attemptTimeout.
func TestJiraBackoffRespectsBudget(t *testing.T) {
	fastJiraRetry(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()
	b := jiraRefreshRetry
	b.budget, b.attemptTimeout = 200*time.Millisecond, 5*time.Second
	start := time.Now()
	if _, _, err := b.post(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("hung endpoint reported success")
	}
	if el := time.Since(start); el > 2*time.Second || el < 150*time.Millisecond {
		t.Errorf("took %v, want about the 200ms budget", el)
	}
}

func TestEqualJitterBounds(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := equalJitter(time.Second)
		if d < 500*time.Millisecond || d > time.Second {
			t.Fatalf("jitter %v outside [d/2, d]", d)
		}
	}
}

func TestJiraCallbackPageOnExchangeFailure(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		tx := oauthCallbackText(lang, "Jira")
		if tx.codeConsumed == "" || tx.tryAgain == "" || tx.backLink == "" {
			t.Fatalf("%s: missing strings %+v", lang, tx)
		}
		rec := httptest.NewRecorder()
		bbCallbackPageLink(rec, tx.codeConsumed, tx.backLink)
		body := rec.Body.String()
		if !strings.Contains(body, `href="/"`) || !strings.Contains(body, tx.backLink) {
			t.Errorf("%s: page has no link back: %s", lang, body)
		}
	}
	if err := jiraCodeConsumed(errors.New("Post x: connection reset by peer")); err {
		t.Error("a transport error is not a consumed code")
	}
}

// A response whose headers arrive and whose body is cut by a reset is a transport failure:
// the partial answer is discarded and the grant retried. For a refresh that means presenting
// the same, possibly already rotated, token again, which is safe only inside Atlassian's
// reuse interval (10 minutes); this test is the anchor for that dependency.
func TestJiraRefreshGrantRetriesAfterMidBodyReset(t *testing.T) {
	fastJiraRetry(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Content-Length", "200")
			_, _ = w.Write([]byte(`{"access_token":"at-partial","refresh_tok`))
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","expires_in":3600}`))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()
	tok, aerr := jiraRefreshGrant(context.Background(), "cid", "s", "rt1")
	if aerr != nil || tok.AccessToken != "at2" || tok.RefreshToken != "rt2" || hits != 2 {
		t.Fatalf("tok=%+v aerr=%v hits=%d, want the partial body discarded and a retry", tok, aerr, hits)
	}
}

// 408 and 425 are retried; the refresh must not call them a refused grant.
func TestJiraRefreshGrantTreatsProxyTimeoutsAsTransient(t *testing.T) {
	fastJiraRetry(t)
	for _, code := range []int{408, 425} {
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(code)
		}))
		old := jiraTokenURL
		jiraTokenURL = srv.URL
		_, aerr := jiraRefreshGrant(context.Background(), "cid", "s", "rt1")
		jiraTokenURL = old
		srv.Close()
		if aerr == nil || aerr.code == "invalid_grant" || hits != jiraRefreshRetry.attempts {
			t.Errorf("status %d: aerr=%+v hits=%d, want retries then jira_error", code, aerr, hits)
		}
	}
}

// A provider's error body is echoed to the page and the API error; it is clipped.
func TestJiraRefusalBodyIsClipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 100000)))
	}))
	defer srv.Close()
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()
	_, aerr := jiraRefreshGrant(context.Background(), "cid", "s", "rt1")
	if aerr == nil || len(aerr.message) > echoLimit+200 {
		t.Errorf("refresh message not clipped: %d", len(aerr.message))
	}
	_, err := jiraExchangeCode(context.Background(), "cid", "s", "c", "https://af.example/cb")
	if err == nil || len(err.Error()) > echoLimit {
		t.Errorf("exchange message not clipped")
	}
}

// Closing the tab cancels the exchange instead of letting it run out its budget.
func TestJiraExchangeCodeHonoursContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)
	old := jiraTokenURL
	jiraTokenURL = srv.URL
	defer func() { jiraTokenURL = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := jiraExchangeCode(ctx, "cid", "s", "c", "https://af.example/cb"); err == nil {
		t.Fatal("cancelled exchange reported success")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v after cancel", el)
	}
}
