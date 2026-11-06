package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPathFormRedirectsWhenPreviewDomainSet(t *testing.T) {
	hit := false
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer agent.Close()
	mux, mgr := newPreviewTenantTestEnvMgr(t, agent.URL)
	mgr.previewDomain = "preview.example.test"

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/app/index.html?a=1&tenant=security&b=x%20y", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Path != previewOwnOpenPath {
		t.Fatalf("Location path=%q, want %q", loc.Path, previewOwnOpenPath)
	}
	q := loc.Query()
	if q.Get("port") != "8080" || q.Get("tenant") != "security" {
		t.Fatalf("Location query=%v", q)
	}
	if got := q.Get("next"); got != "/app/index.html?a=1&b=x%20y" {
		t.Fatalf("next=%q", got)
	}
	if hit {
		t.Fatal("the workspace was reached on the Console origin")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("redirect must not mint cookies")
	}

	// A write method is refused rather than redirected into a GET-only entry.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/preview/8080/api", strings.NewReader("x")))
	if rec.Code != http.StatusMethodNotAllowed || hit {
		t.Fatalf("POST: status=%d hit=%v", rec.Code, hit)
	}
	// A port that is not one is refused, not forwarded to the entry.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/99999/x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad port: status=%d", rec.Code)
	}
}

func TestPathFormSandboxedWithoutPreviewDomain(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Add("Set-Cookie", "app=1; Path=/")
		w.Header().Add("Set-Cookie", "_oauth2_proxy=x; Path=/")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		w.Header().Set("X-App", "kept")
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	mux, _ := newPreviewTenantTestEnvMgr(t, agent.URL)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/8080/?tenant=default", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	csps := rec.Header().Values("Content-Security-Policy")
	if len(csps) != 2 || csps[0] != "default-src 'self'" || csps[1] != pathFormSandboxCSP {
		t.Fatalf("CSP=%q", csps)
	}
	if strings.Contains(pathFormSandboxCSP, "allow-same-origin") {
		t.Fatal("sandbox must not allow same-origin")
	}
	for _, h := range []string{"Service-Worker-Allowed", "Clear-Site-Data"} {
		if v := rec.Header().Get(h); v != "" {
			t.Fatalf("%s=%q leaked", h, v)
		}
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name != previewTenantCookie {
			t.Fatalf("app cookie %q leaked through the path form", c.Name)
		}
	}
	if rec.Header().Get("X-App") != "kept" {
		t.Fatal("other app headers must pass")
	}
}

func TestPathFormWebSocketOriginRefused(t *testing.T) {
	hit := false
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer agent.Close()
	mux, _ := newPreviewTenantTestEnvMgr(t, agent.URL)

	for _, origin := range []string{"https://evil.example", "null"} {
		req := httptest.NewRequest(http.MethodGet, "/preview/8080/ws?tenant=default", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("origin %q: status=%d, want 403", origin, rec.Code)
		}
	}
	if hit {
		t.Fatal("a refused handshake reached the workspace")
	}

	// Same-origin handshake still goes through to the relay.
	req := httptest.NewRequest(http.MethodGet, "/preview/8080/ws?tenant=default", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || !hit {
		t.Fatalf("same-origin handshake: status=%d hit=%v", rec.Code, hit)
	}
}

func TestPathFormRedirectKeepsPathAndQueryVerbatim(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer agent.Close()
	mux, mgr := newPreviewTenantTestEnvMgr(t, agent.URL)
	mgr.previewDomain = "preview.example.test"

	next := func(target string, hdr map[string]string) (string, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: status=%d", target, rec.Code)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc.Query().Get("next"), loc.Query().Get("tenant")
	}
	for _, c := range []struct{ target, want string }{
		{"/preview/8080/a%3Fb%23c/d%2Fe.txt", "/a%3Fb%23c/d%2Fe.txt"},                    // reserved chars stay encoded
		{"/%70review/%38080/a%20b", "/a%20b"},                                            // encoded fixed part and port
		{"/preview/8080/", "/"},                                                          // root
		{"/preview/8080/x?b=2&a=1&a=0&s=1;t=2&tenant=default", "/x?b=2&a=1&a=0&s=1;t=2"}, // order, dups, ";"
	} {
		if got, _ := next(c.target, nil); got != c.want {
			t.Errorf("%s: next=%q, want %q", c.target, got, c.want)
		}
	}
	// The header outranks a stale ?tenant=, as everywhere else.
	if _, tn := next("/preview/8080/?tenant=default", map[string]string{"X-AF-Tenant": "security"}); tn != "security" {
		t.Errorf("tenant=%q, want the header's", tn)
	}
}
