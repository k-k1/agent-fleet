package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// secondOrigin records every request it receives (Authorization and body), so a test can say
// that a redirect delivered nothing there.
type secondOrigin struct {
	*httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	seen []string
}

func newSecondOrigin(t *testing.T) *secondOrigin {
	s := &secondOrigin{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Get("Authorization")+"|"+string(b))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"stolen","refresh_token":"stolen","expires_in":60}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *secondOrigin) assertUntouched(t *testing.T, what string) {
	t.Helper()
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%s: the second origin received %d request(s): %q", what, n, s.seen)
	}
}

// redirecting answers every request with code to target and counts its own hits.
func redirecting(t *testing.T, code int, target string, hits *atomic.Int32) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, target, code)
	}))
	t.Cleanup(s.Close)
	return s
}

var redirectCodes = []int{301, 302, 303, 307, 308}

func TestBitbucketRefreshGrantRefusesRedirectAndDoesNotRetry(t *testing.T) {
	for _, code := range redirectCodes {
		other := newSecondOrigin(t)
		var first atomic.Int32
		bb := redirecting(t, code, other.URL+"/stolen", &first)
		old := bbTokenURL
		bbTokenURL = bb.URL
		_, aerr := bbRefreshGrant("key", "client-secret", "refresh-secret")
		bbTokenURL = old
		if aerr == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		if aerr.status == http.StatusBadGateway {
			t.Errorf("%d: a refusal must not look transient to the Agent (502): %+v", code, aerr)
		}
		if strings.Contains(aerr.message, "client-secret") || strings.Contains(aerr.message, "refresh-secret") {
			t.Errorf("%d: message quotes a secret: %q", code, aerr.message)
		}
		other.assertUntouched(t, "bitbucket refresh")
		if n := first.Load(); n != 1 {
			t.Errorf("%d: token endpoint saw %d requests, want 1 (not retried)", code, n)
		}
	}
}

func TestJiraRefreshGrantRefusesRedirectAndDoesNotRetry(t *testing.T) {
	fastJiraRetry(t)
	for _, code := range redirectCodes {
		other := newSecondOrigin(t)
		var first atomic.Int32
		at := redirecting(t, code, other.URL+"/stolen", &first)
		old := jiraTokenURL
		jiraTokenURL = at.URL
		_, aerr := jiraRefreshGrant(context.Background(), "cid", "client-secret", "refresh-secret")
		jiraTokenURL = old
		if aerr == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		if aerr.status == http.StatusBadGateway {
			t.Errorf("%d: a refusal must not look transient to the Agent (502): %+v", code, aerr)
		}
		other.assertUntouched(t, "jira refresh")
		if n := first.Load(); n != 1 {
			t.Errorf("%d: token endpoint saw %d requests, want 1 (not retried)", code, n)
		}
	}
}

func TestJiraExchangeCodeRefusesRedirectAndDoesNotRetry(t *testing.T) {
	fastJiraRetry(t)
	for _, code := range redirectCodes {
		other := newSecondOrigin(t)
		var first atomic.Int32
		at := redirecting(t, code, other.URL+"/stolen", &first)
		old := jiraTokenURL
		jiraTokenURL = at.URL
		_, err := jiraExchangeCode(context.Background(), "cid", "client-secret", "code", "https://af.example/cb")
		jiraTokenURL = old
		if err == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		other.assertUntouched(t, "jira exchange")
		if n := first.Load(); n != 1 {
			t.Errorf("%d: token endpoint saw %d requests, want 1 (not retried)", code, n)
		}
	}
}

// The Bitbucket code exchange (oauth_bitbucket.go) and the device grant POST through these
// clients directly, with Basic client credentials / a form body.
func TestCredentialClientsRefuseRedirectedPost(t *testing.T) {
	clients := map[string]*http.Client{
		"bbHTTPClient":       bbHTTPClient,
		"ghDeviceHTTPClient": ghDeviceHTTPClient,
		"egress batcher":     newEgressBatcher("http://unused.invalid", "tok").client,
	}
	for name, c := range clients {
		for _, code := range redirectCodes {
			other := newSecondOrigin(t)
			var first atomic.Int32
			src := redirecting(t, code, other.URL+"/stolen", &first)
			req, _ := http.NewRequest(http.MethodPost, src.URL, strings.NewReader("client_secret=s&refresh_token=r"))
			req.SetBasicAuth("key", "secret")
			req.Header.Set("Authorization", "Bearer tok")
			resp, err := c.Do(req)
			if err == nil {
				resp.Body.Close()
				t.Errorf("%s %d: redirected POST was not refused (status %d)", name, code, resp.StatusCode)
			}
			other.assertUntouched(t, name)
		}
	}
}
