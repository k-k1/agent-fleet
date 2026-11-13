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

// A write that a server redirects must not be re-sent as a GET: its 200 would read as success.
func TestJiraRedirectedWriteIsRefused(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			var hits atomic.Int32
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path == "/old" {
					http.Redirect(w, r, "/new", code)
					return
				}
				w.WriteHeader(200)
			}))
			swapJiraClient(t, src.Client())
			res := jiraDo(redirectCreds, method, src.URL+"/old", []byte(`{}`))
			src.Close()
			if res.err == nil || res.status == 200 {
				t.Errorf("%s %d: status=%d err=%v", method, code, res.status, res.err)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("%s %d: origin saw %d requests, want 1", method, code, n)
			}
		}
	}
}

// An endless same-origin chain, and a hop to another origin at the 10th step, are both refused
// without re-sending the chain, and the error never quotes the target URL.
func TestJiraRedirectLimitIsNotRetried(t *testing.T) {
	other := newSink(t, false)
	var hits atomic.Int32
	var toOther bool
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if toOther && n%10 == 0 {
			http.Redirect(w, r, other.URL+"/?token=redirect-secret", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer src.Close()
	swapJiraClient(t, src.Client())
	for _, cross := range []bool{false, true} {
		hits.Store(0)
		toOther = cross
		res := jiraDo(redirectCreds, http.MethodGet, src.URL+"/loop", nil)
		if res.err == nil || !strings.Contains(res.err.Error(), "redirect refused") || strings.Contains(res.err.Error(), "redirect-secret") {
			t.Errorf("cross=%v err = %v", cross, res.err)
		}
		if n := hits.Load(); n > 10 {
			t.Errorf("cross=%v: chain was re-sent, origin saw %d requests", cross, n)
		}
		if other.hits.Load() != 0 {
			t.Errorf("cross=%v: other origin was reached", cross)
		}
	}
}

func TestJiraOriginKeepsIPv6ZoneCase(t *testing.T) {
	p := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	if jiraOrigin(p("http://[fe80::1%25ethA]/")) == jiraOrigin(p("http://[fe80::1%25etha]/")) {
		t.Error("zones differing in case are different interfaces")
	}
	if jiraOrigin(p("http://[FE80::AB]/")) != jiraOrigin(p("http://[fe80::ab]/")) {
		t.Error("IPv6 hex case must not matter")
	}
}

func TestJiraOriginDoesNotFoldUnicodeHosts(t *testing.T) {
	p := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	if jiraOrigin(p("http://\u0130.example.test/")) == jiraOrigin(p("http://i.example.test/")) {
		t.Error("U+0130 must not fold onto ASCII i")
	}
	if jiraOrigin(p("https://EXAMPLE.test/")) != jiraOrigin(p("https://example.test/")) {
		t.Error("ASCII case must fold")
	}
}
