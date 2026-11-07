package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// relayAllowed: legitimate escaped characters stay escaped all the way to the Agent.
// relayRefused: an encoded separator (any case, double-encoded too), a dot segment or an
// interior empty segment is refused before anything is sent.
var (
	relayAllowed = []string{
		"/api/sessions/a%20b/input",
		"/api/fs/%E3%81%82%E3%81%84/x",
		"/api/fs/100%25/x",
		"/api/fs/a+b/x",
	}
	relayRefused = []string{
		"/api/sessions/a%2Fb/input",
		"/api/sessions/a%2fb/input",
		"/api/sessions/a%3Fb/input",
		"/api/sessions/a%3fb/input",
		"/api/sessions/a%23b/input",
		"/api/sessions/a%252Fb/input",
		"/api/sessions/a%252fb/input",
		"/api/sessions/a%253Fb/input",
		"/api/sessions/a%2523b/input",
		"/api/sessions/%2e%2e/input",
		"/api/sessions/%2E%2E%2Fadmin",
		"/api/sessions//input",
		"/api/sessions/bad%zz/input",
	}
)

func TestRelayTargetPath(t *testing.T) {
	for _, p := range relayAllowed {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		got, ok := relayTargetPath(r)
		if !ok || got != strings.TrimPrefix(p, "/api") {
			t.Errorf("relayTargetPath(%q) = %q, %v", p, got, ok)
		}
	}
	for _, p := range relayRefused {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path, r.URL.RawPath = "", ""
		if u, err := r.URL.Parse(p); err == nil {
			r.URL = u
		} else {
			continue // httptest cannot even build a request with an invalid escape
		}
		if got, ok := relayTargetPath(r); ok {
			t.Errorf("relayTargetPath(%q) = %q, want refusal", p, got)
		}
	}
}

// The REST relay and the stream relay forward the escaped path and never reach the Agent with
// a request whose path was re-joined from decoded segments.
func TestRelayRestAndStreamForwardEscapedPath(t *testing.T) {
	var got []string
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.EscapedPath())
	}))
	defer cleanup()
	for name, do := range map[string]func(http.ResponseWriter, *http.Request, *resolved){"rest": proxy.rest, "stream": proxy.stream} {
		for _, p := range relayAllowed {
			got = nil
			rec := httptest.NewRecorder()
			do(rec, httptest.NewRequest(http.MethodGet, p, nil), res)
			if len(got) != 1 || got[0] != strings.TrimPrefix(p, "/api") {
				t.Errorf("%s %q: Agent saw %v (status %d)", name, p, got, rec.Code)
			}
		}
		for _, p := range relayRefused {
			if !validTestRequestPath(p) {
				continue
			}
			got = nil
			rec := httptest.NewRecorder()
			do(rec, httptest.NewRequest(http.MethodGet, p, nil), res)
			if len(got) != 0 || rec.Code != http.StatusBadRequest {
				t.Errorf("%s %q: Agent saw %v, status %d, want 400 and no request", name, p, got, rec.Code)
			}
		}
	}
}

func validTestRequestPath(p string) bool {
	return !strings.Contains(p, "%zz")
}

func TestBrowserAgentHTTPURLKeepsEscapedPath(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/browser/pages/a%20b?tenant=t&x=1", nil)
	got, err := browserAgentHTTPURL("http://agent:1", r)
	if err != nil || got != "http://agent:1/browser/pages/a%20b?x=1" {
		t.Fatalf("got %q, %v", got, err)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/browser/pages/a%2Fb", nil)
	if got, err := browserAgentHTTPURL("http://agent:1", r); err == nil {
		t.Fatalf("encoded separator accepted: %q", got)
	}
}

func TestCanonicalRoutePath(t *testing.T) {
	for in, want := range map[string]string{
		"/":                    "/",
		"/login":               "/login",
		"/brand/a%20b.png":     "/brand/a b.png",
		"/%6Cogin":             "/login",
		"/brand/%2e%2e/secret": "/secret",
		"/brand/../x":          "/x",
		"/brand//x":            "/brand/x",
		"/login/":              "/login/",
	} {
		if got, ok := canonicalRoutePath(in); !ok || got != want {
			t.Errorf("canonicalRoutePath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/brand/a%2Fb", "/brand/a%2fb", "/brand/%zz"} {
		if got, ok := canonicalRoutePath(in); ok {
			t.Errorf("canonicalRoutePath(%q) = %q, want refusal", in, got)
		}
	}
}

// Exemption and routing agree: every exempt prefix and its encoded variants.
func TestAuthExemptEncodedVariants(t *testing.T) {
	smokeEnv(t)
	table := []struct {
		path string
		want bool
	}{
		{"/oauth2/callback", true},
		{"/%6Fauth2/callback", true}, // the mux decodes each segment too, so it routes here
		{"/oauth2/a%20b", true},
		{"/brand/logo.png", true},
		{"/brand/..%2findex.html", false},
		{"/brand/..%2Findex.html", false},
		{"/brand/%2e%2e/index.html", false},
		{"/brand/%2e%2e%2findex.html", false},
		{"/brand/../index.html", false},
		{"/login/acme", true},
		{"/login/..%2f..%2findex.html", false},
		{"/login/%2E%2E/%2E%2E/x", false},
		{"/oauth2%2f..%2findex.html", false},
		{"/login", true},
		{"/login%2fx", false},
		{"/healthz", true},
		{"/%68ealthz", true},
		{"/healthz%2f", false},
		{"/", false},
		{"/index.html", false},
		{"/assets/app.js", false},
		{"/api/sessions", false},
		{"/%zz", false},
	}
	for _, c := range table {
		if got := isAuthExempt(c.path); got != c.want {
			t.Errorf("isAuthExempt(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// Through the real gate: an encoded path that starts with an exempt prefix must not reach the
// static handler without a session, while the plain static path is gated the same way.
func TestAuthGateEncodedExemptPrefixNeedsSession(t *testing.T) {
	cfg, _ := smokeEnv(t)
	var reached atomic.Int32
	gate := cfg.authGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	for _, p := range []string{"/brand/..%2findex.html", "/login/..%2f..%2findex.html", "/index.html", "/"} {
		reached.Store(0)
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if reached.Load() != 0 {
			t.Errorf("%q reached the handler without a session (status %d)", p, rec.Code)
		}
	}
	reached.Store(0)
	gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/brand/logo.png", nil))
	if reached.Load() != 1 {
		t.Error("/brand/logo.png must stay reachable without a session")
	}
}

// Every CP→Agent / engine client built here refuses a redirect and hands the 3xx back.
func TestAgentClientsRefuseRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer redirector.Close()
	clients := map[string]*http.Client{
		"agentHTTPClient":       agentHTTPClient,
		"agentLongCallClient":   agentLongCallClient,
		"agentRelayClient":      agentRelayClient,
		"healthzClient":         healthzClient,
		"agentStatsClient":      agentStatsClient,
		"homeKeyHealthzClient":  homeKeyHealthzClient,
		"previewListenerClient": previewListenerClient,
		"engineUsageClient":     engineUsageClient,
		"gitTokenPushClient":    gitTokenPushClient,
		"newAgentClient":        newAgentClient(time.Second, newAgentTransport()),
	}
	for name, c := range clients {
		resp, err := c.Get(redirector.URL)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("%s: status %d, want the 302 handed back", name, resp.StatusCode)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("a client followed the redirect (%d hits)", hits.Load())
	}
}
