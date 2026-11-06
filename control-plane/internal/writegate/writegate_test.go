package writegate

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPolicyCheck(t *testing.T) {
	p := Policy{
		Exempt: func(path string) bool {
			for _, pre := range []string{"/internal/", "/git/", "/engine/", "/oauth2/", "/mcp"} {
				if strings.HasPrefix(path, pre) {
					return true
				}
			}
			return false
		},
		SameOrigin: func(r *http.Request) bool {
			u, err := url.Parse(r.Header.Get("Origin"))
			return err == nil && u.Host == r.Host
		},
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rej := p.Check(r); rej != nil {
			w.WriteHeader(rej.Status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	type hdr map[string]string
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		h      hdr
		want   int
	}{
		// rejected
		{"cross-site fetch metadata", "POST", "/api/sessions", `{}`, hdr{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, 403},
		{"same-site fetch metadata", "POST", "/api/sessions", `{}`, hdr{"Sec-Fetch-Site": "same-site", "Content-Type": "application/json"}, 403},
		{"foreign origin", "POST", "/api/sessions", `{}`, hdr{"Origin": "https://evil.example.org", "Content-Type": "application/json"}, 403},
		{"foreign origin with same-origin metadata", "POST", "/api/sessions", `{}`, hdr{"Origin": "https://evil.example.org", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, 403},
		{"null origin", "DELETE", "/api/sessions/x", "", hdr{"Origin": "null"}, 403},
		{"same-site origin on a sibling host", "PUT", "/api/fs/file", `{}`, hdr{"Origin": "https://p-3000.console.test", "Content-Type": "application/json"}, 403},
		{"text/plain body", "POST", "/api/sessions", `{}`, hdr{"Content-Type": "text/plain"}, 415},
		{"urlencoded body", "POST", "/api/sessions", `a=b`, hdr{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		{"body without content type", "POST", "/api/sessions", `{}`, hdr{}, 415},
		// legitimate
		{"console JSON", "POST", "/api/sessions", `{}`, hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, 204},
		{"JSON with charset", "PUT", "/api/fs/file", `{}`, hdr{"Origin": "https://console.test", "Content-Type": "application/json; charset=utf-8"}, 204},
		{"multipart upload", "POST", "/api/fs/upload", `x`, hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin", "Content-Type": "multipart/form-data; boundary=x"}, 204},
		{"bodyless POST", "POST", "/api/fs/mkdir", "", hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin"}, 204},
		{"bodyless DELETE", "DELETE", "/api/sessions/x", "", hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin"}, 204},
		{"user-initiated none", "POST", "/api/sessions", `{}`, hdr{"Sec-Fetch-Site": "none", "Content-Type": "application/json"}, 204},
		{"non-browser caller", "POST", "/api/sessions", `{}`, hdr{"Content-Type": "application/json"}, 204},
		{"dev-idp consent form", "POST", "/dev-idp/consent", `a=b`, hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}, 204},
		{"preview app form", "POST", "/preview/3000/save", `a=b`, hdr{"Origin": "https://console.test", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}, 204},
		{"cross-site GET", "GET", "/api/sessions", "", hdr{"Sec-Fetch-Site": "cross-site"}, 204},
		// exempt: not cookie-authenticated
		{"internal route", "POST", "/internal/x", `{}`, hdr{"Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"}, 204},
		{"git smart HTTP", "POST", "/git/s/r/git-upload-pack", `x`, hdr{"Content-Type": "application/x-git-upload-pack-request"}, 204},
		{"mcp", "POST", "/mcp", `{}`, hdr{"Origin": "https://other.example.org"}, 204},
		{"engine gateway", "POST", "/engine/k/v1/chat", `{}`, hdr{"Origin": "https://other.example.org"}, 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "https://console.test"+c.path, strings.NewReader(c.body))
			for k, v := range c.h {
				r.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestConsoleFrameHeaders(t *testing.T) {
	h := FrameHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q", rec.Header().Get("X-Frame-Options"))
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'self'", "object-src 'none'", "base-uri 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src") {
		t.Errorf("script-src must not be enforced yet: %q", csp)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/preview/3000/", nil))
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Errorf("the user's preview app keeps its own framing policy")
	}
}
