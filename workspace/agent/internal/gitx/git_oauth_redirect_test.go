package gitx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/redirectpolicy"
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

// BitbucketGetStatus retries transport errors with a backoff; a refused redirect is not one.
func TestBitbucketGetStatusRedirectRefusalIsNotRetried(t *testing.T) {
	other := newCredSink(t)
	var first atomic.Int32
	bb := redirectingOrigin(t, 302, other.URL+"/stolen", &first)
	_, _, err := BitbucketGetStatus(redirectpolicy.Client(5*time.Second), "Bearer secret", bb.URL+"/2.0/user")
	if err == nil {
		t.Fatal("a redirect off the origin was followed")
	}
	other.assertUntouched(t, "bitbucket GET")
	if n := first.Load(); n != 1 {
		t.Errorf("origin saw %d requests, want 1 (not retried)", n)
	}
}

// With legacy client credentials still stored, a bridge failure falls back to the direct grant.
// A redirect refusal (the CP's own 307, or the CP reporting its upstream one) must not: it would
// present the same refresh token again.
func TestBitbucketGrantDoesNotFallBackAfterRedirectRefusal(t *testing.T) {
	var directHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":7200}`))
	}))
	defer direct.Close()
	old := bbTokenURL
	bbTokenURL = direct.URL
	defer func() { bbTokenURL = old }()

	other := newCredSink(t)
	cases := map[string]http.HandlerFunc{
		"CP redirects": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/stolen", http.StatusTemporaryRedirect)
		},
		"CP reports upstream refusal": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"bitbucket_redirect_refused","message":"x"}}`))
		},
	}
	for name, h := range cases {
		withAgentHome(t)
		cp := httptest.NewServer(h)
		if err := secrets.Update(func(cur *secrets.Data) error {
			cur.GitOAuthBridge = &secrets.CPBridge{BaseURL: cp.URL, Token: "afo_tok"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		directHits.Store(0)
		_, err := refreshBitbucketGrant(secrets.BitbucketCreds{RefreshToken: "r0", Key: "k", Secret: "s"})
		cp.Close()
		if err == nil {
			t.Errorf("%s: the grant succeeded", name)
		}
		if n := directHits.Load(); n != 0 {
			t.Errorf("%s: direct grant ran %d time(s)", name, n)
		}
		other.assertUntouched(t, name)
	}
}
