package jirax

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// swapJiraClient points jiraHTTPClient at c, keeping the production CheckRedirect.
func swapJiraClient(t *testing.T, c *http.Client) {
	t.Helper()
	old := jiraHTTPClient
	c.CheckRedirect = old.CheckRedirect
	jiraHTTPClient = c
	t.Cleanup(func() { jiraHTTPClient = old })
}

// sink records every request it receives, with the Authorization header each carried.
type sink struct {
	*httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	auth []string
}

func newSink(t *testing.T, tls bool) *sink {
	s := &sink{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.WriteHeader(200)
	})
	if tls {
		s.Server = httptest.NewTLSServer(h)
	} else {
		s.Server = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	return s
}

func redirector(t *testing.T, tls bool, to func() string) *httptest.Server {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to(), http.StatusTemporaryRedirect)
	})
	var s *httptest.Server
	if tls {
		s = httptest.NewTLSServer(h)
	} else {
		s = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	return s
}

var redirectCreds = &secrets.JiraCreds{Site: "https://example.atlassian.net", Email: "a@example.com", Token: "secret-token"}

func TestJiraRedirectLeavingOriginSendsNoCredential(t *testing.T) {
	other := newSink(t, false)
	tlsOther := newSink(t, true)
	u, _ := url.Parse(other.URL)
	cases := []struct {
		name   string
		tls    bool
		target func() string
		sink   *sink
	}{
		{"another port", false, func() string { return other.URL + "/x" }, other},
		{"another host name", false, func() string { return "http://localhost:" + u.Port() + "/x" }, other},
		{"HTTPS to plain HTTP", true, func() string { return other.URL + "/x" }, other},
		{"HTTPS to another HTTPS port", true, func() string { return tlsOther.URL + "/x" }, tlsOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.sink.hits.Store(0)
			src := redirector(t, tc.tls, tc.target)
			swapJiraClient(t, src.Client())
			var res jiraCallResult
			res = jiraDo(redirectCreds, http.MethodGet, src.URL+"/rest/api/3/myself", nil)
			if res.err == nil {
				t.Fatalf("redirect left the origin and the call succeeded (status %d)", res.status)
			}
			if !strings.Contains(res.err.Error(), "redirect") || strings.Contains(res.err.Error(), "secret-token") {
				t.Errorf("err = %v", res.err)
			}
			if n := tc.sink.hits.Load(); n != 0 {
				t.Errorf("second origin received %d requests, auth=%q", n, tc.sink.auth)
			}
		})
	}
}

// A refused redirect is deterministic: retrying a GET would only repeat the first request.
func TestJiraRedirectRefusalIsNotRetried(t *testing.T) {
	other := newSink(t, false)
	var first atomic.Int32
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer src.Close()
	swapJiraClient(t, src.Client())
	jiraDo(redirectCreds, http.MethodGet, src.URL+"/x", nil)
	if n := first.Load(); n != 1 {
		t.Errorf("origin saw %d requests, want 1", n)
	}
}

func TestJiraSameOriginRedirectIsFollowed(t *testing.T) {
	var gotAuth string
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer src.Close()
	swapJiraClient(t, src.Client())
	res := jiraDo(redirectCreds, http.MethodGet, src.URL+"/old", nil)
	if res.err != nil || res.status != 200 {
		t.Fatalf("status=%d err=%v", res.status, res.err)
	}
	if gotAuth != jiraAuthHeader(redirectCreds) {
		t.Errorf("same-origin hop lost the header: %q", gotAuth)
	}
}

func TestJiraOriginNormalisesDefaultPort(t *testing.T) {
	p := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	if jiraOrigin(p("https://Example.com/a")) != jiraOrigin(p("https://example.com:443/b")) {
		t.Error("default https port should compare equal")
	}
	for _, o := range []string{"http://example.com", "https://example.com:8443", "https://sub.example.com"} {
		if jiraOrigin(p("https://example.com")) == jiraOrigin(p(o)) {
			t.Errorf("%s must differ", o)
		}
	}
}
