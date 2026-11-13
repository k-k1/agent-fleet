package gitx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// credSink is a second origin that records everything it is sent.
type credSink struct {
	*httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	seen []string // Authorization header and body of each request
}

func newCredSink(t *testing.T) *credSink {
	s := &credSink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Get("Authorization")+"|"+string(b))
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"stolen","expires_in":60}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// redirectingOrigin answers every request with code to target, and counts its own hits.
func redirectingOrigin(t *testing.T, code int, target string, hits *atomic.Int32) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, target, code)
	}))
	t.Cleanup(s.Close)
	return s
}

var redirectCodes = []int{301, 302, 303, 307, 308}

func (s *credSink) assertUntouched(t *testing.T, what string) {
	t.Helper()
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%s: the second origin received %d request(s): %q", what, n, s.seen)
	}
}

func TestCPRefreshRedirectIsRefused(t *testing.T) {
	for _, code := range redirectCodes {
		other := newCredSink(t)
		var first atomic.Int32
		cp := redirectingOrigin(t, code, other.URL+"/stolen", &first)
		_, err := RefreshOAuthViaCPContext(context.Background(), secrets.CPBridge{BaseURL: cp.URL, Token: "afo_tok"}, "jira", "refresh-secret")
		if err == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		if CPRefreshTransient(err) {
			t.Errorf("%d: a refused redirect must not be retried as transient: %v", code, err)
		}
		other.assertUntouched(t, "jira bridge")
		if n := first.Load(); n != 1 {
			t.Errorf("%d: CP saw %d requests, want 1", code, n)
		}
	}
}

func TestBitbucketCPRefreshRedirectIsRefused(t *testing.T) {
	for _, code := range redirectCodes {
		other := newCredSink(t)
		var first atomic.Int32
		cp := redirectingOrigin(t, code, other.URL+"/stolen", &first)
		_, err := refreshBitbucketViaCPContext(context.Background(), secrets.CPBridge{BaseURL: cp.URL, Token: "afo_tok"},
			secrets.BitbucketCreds{RefreshToken: "refresh-secret"})
		if err == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		other.assertUntouched(t, "bitbucket bridge")
	}
}

// The direct grant retries transport errors; a refused redirect is not one, and must not be re-sent.
func TestBitbucketDirectRefreshRedirectIsRefusedAndNotRetried(t *testing.T) {
	for _, code := range redirectCodes {
		other := newCredSink(t)
		var first atomic.Int32
		bb := redirectingOrigin(t, code, other.URL+"/stolen", &first)
		old := bbTokenURL
		bbTokenURL = bb.URL
		_, err := refreshBitbucketDirect(context.Background(), secrets.BitbucketCreds{Key: "k", Secret: "client-secret", RefreshToken: "refresh-secret"})
		bbTokenURL = old
		if err == nil {
			t.Fatalf("%d: a redirected POST succeeded", code)
		}
		other.assertUntouched(t, "bitbucket direct")
		if n := first.Load(); n != 1 {
			t.Errorf("%d: token endpoint saw %d requests, want 1 (not retried)", code, n)
		}
	}
}
