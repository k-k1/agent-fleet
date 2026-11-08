package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/writegate"
)

// Executable <script> elements: those without type, or with a JavaScript type. A
// type="application/json" data block is not executed and is not subject to script-src.
var scriptTagRe = regexp.MustCompile(`(?is)<script\b([^>]*)>`)

func executableInlineScripts(html string) []string {
	var out []string
	for _, m := range scriptTagRe.FindAllStringSubmatch(html, -1) {
		attrs := strings.ToLower(m[1])
		if strings.Contains(attrs, "src=") {
			continue
		}
		if strings.Contains(attrs, "type=") && !strings.Contains(attrs, "module") && !strings.Contains(attrs, "javascript") {
			continue
		}
		out = append(out, m[0])
	}
	return out
}

// The Console's CSP says script-src 'self': an inline script in the shell would be blocked and the
// page would load with a wrong <base> (or none of its code).
func TestConsoleShellHasNoExecutableInlineScript(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "console", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got := executableInlineScripts(string(src)); len(got) != 0 {
		t.Errorf("console/index.html has inline script: %v", got)
	}
	s := string(src)
	boot, entry := strings.Index(s, `src="?af-bootstrap"`), strings.Index(s, `type="module"`)
	if boot < 0 || entry < 0 || boot > entry {
		t.Errorf("the <base> bootstrap must be a classic script ahead of the module entry (boot=%d entry=%d)", boot, entry)
	}
	// Self-test of the scanner: it must catch what it is looking for.
	if len(executableInlineScripts("<script>x()</script>")) != 1 || len(executableInlineScripts(`<script type="application/json">{}</script>`)) != 0 {
		t.Error("executableInlineScripts is not discriminating")
	}
}

func TestBrandedShellHasNoExecutableInlineScript(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "console", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := newBrandConfig("violet", "dev").rewriteIndexHTML(src)
	if got := executableInlineScripts(string(out)); len(got) != 0 {
		t.Errorf("branded shell has inline script: %v", got)
	}
}

// "?af-bootstrap" must be answered with the script at every path that serves the shell, because the
// bootstrap's whole job is to run where a path-relative URL would resolve outside the mount.
func TestStaticServesBaseBootstrapAtShellPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base-bootstrap.js"), []byte("/*bootstrap*/"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerStatic(mux, config{consoleDir: dir})
	for _, p := range []string{"/?af-bootstrap", "/open/browser-attachment/abc?af-bootstrap", "/open/browser-attachment/abc/?af-bootstrap"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || rec.Body.String() != "/*bootstrap*/" {
			t.Errorf("GET %s = %d %q", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("GET %s Content-Type = %q", p, ct)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "shell") {
		t.Errorf("plain / must still be the shell: %q", rec.Body.String())
	}
}

// The two built draw.io documents go through the real handler chain with no cookie: served 200 with
// their own CSP (including sandbox, so the server response alone guarantees an opaque origin), while
// look-alikes and every other Console path stay behind the gate.
func TestDrawioFramesThroughTheRealChain(t *testing.T) {
	cfg, _ := smokeEnvWith(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html":              "<html>shell</html>",
		"drawio-frame-light.html": "<html>light-frame</html>",
		"drawio-frame-dark.html":  "<html>dark-frame</html>",
		"other.html":              "<html>other</html>",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg.consoleDir = dir
	h := cfg.authGate(writegate.FrameHeaders(buildMux(cfg)))
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	for path, body := range map[string]string{
		"/drawio-frame-light.html": "light-frame",
		"/drawio-frame-dark.html":  "dark-frame",
	} {
		rec := get(path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), body) {
			t.Fatalf("GET %s = %d %q", path, rec.Code, rec.Body.String())
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"sandbox allow-scripts", "script-src 'unsafe-inline'", "connect-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Errorf("%s: X-Frame-Options = %q", path, rec.Header().Get("X-Frame-Options"))
		}
	}
	// Not exempt: with no cookie and no Accept header the gate answers 401, exactly. Anything else
	// (200, 404, 5xx, a redirect) means the gate was bypassed or the path reached something.
	for _, path := range []string{
		"/drawio-frame-light.html/", "/drawio-frame-light.html/x", "/Drawio-Frame-Light.html",
		"/DRAWIO-FRAME-DARK.HTML", "/drawio-frame-light.htm", "/drawio-frame-light.html%2f",
		"/drawio-frame-light%2ehtml/../other.html", "/x/%2e%2e/drawio-frame-light.html",
		"/x/%252e%252e/drawio-frame-light.html", "/drawio-frame-light%252ehtml",
		"/x/../other.html", "/other.html", "/", "/api/whoami",
	} {
		if rec := get(path); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d %q, want 401 from the gate", path, rec.Code, rec.Body.String())
		}
	}
	// A browser navigation (Accept: text/html) is sent to the login page instead.
	req := httptest.NewRequest("GET", "/other.html", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("HTML navigation to /other.html = %d Location=%q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
	// A canonical-form redirect is a redirect, not the document.
	rec = get("/x/../drawio-frame-light.html")
	if rec.Code < 300 || rec.Code >= 400 || strings.Contains(rec.Body.String(), "light-frame") {
		t.Errorf("non-canonical path: %d %q", rec.Code, rec.Body.String())
	}
}
