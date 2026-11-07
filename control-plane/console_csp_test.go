package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

func TestDrawioFramesAreAuthExempt(t *testing.T) {
	mux := http.NewServeMux()
	registerStatic(mux, config{consoleDir: t.TempDir()})
	for _, p := range []string{"/drawio-frame-light.html", "/drawio-frame-dark.html"} {
		if !isAuthExempt(p) {
			t.Errorf("%s must load without a session cookie (opaque-origin frame)", p)
		}
	}
}
