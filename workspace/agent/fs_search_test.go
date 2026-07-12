package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// exercises handleFSSearch end-to-end: rg walk, per-repo .gitignore pruning
// (node_modules skipped), denylist, and home-relative path mapping.
func TestHandleFSSearch(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	home := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", home)

	repo := filepath.Join(home, "repos", "myrepo")
	mustMkdir(t, filepath.Join(repo, "src"))
	mustMkdir(t, filepath.Join(repo, "node_modules", "pkg"))
	mustWrite(t, filepath.Join(repo, "src", "index.ts"), "x")
	mustWrite(t, filepath.Join(repo, "README.md"), "x")
	mustWrite(t, filepath.Join(repo, ".gitignore"), "node_modules/\n")
	mustWrite(t, filepath.Join(repo, "node_modules", "pkg", "index.js"), "x") // must be pruned
	// a denylisted path under home (searched only when root=="")
	mustMkdir(t, filepath.Join(home, ".codex"))
	mustWrite(t, filepath.Join(home, ".codex", "index.ts"), "secret")

	// rg honours .gitignore only inside a git repo.
	run(t, repo, "git", "init", "-q")

	search := func(root, q string) (results []string, truncated bool) {
		req := httptest.NewRequest("GET", "/fs/search?path="+root+"&q="+q, nil)
		w := httptest.NewRecorder()
		handleFSSearch(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			Results   []string `json:"results"`
			Truncated bool     `json:"truncated"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Results, out.Truncated
	}

	// "index" matches src/index.ts (home-relative) and NOT node_modules, NOT .codex.
	got, _ := search("repos", "index")
	if !has(got, "repos/myrepo/src/index.ts") {
		t.Errorf("want repos/myrepo/src/index.ts in %v", got)
	}
	for _, g := range got {
		if contains(g, "node_modules") {
			t.Errorf("node_modules leaked: %s", g)
		}
		if contains(g, ".codex") {
			t.Errorf("denylisted .codex leaked: %s", g)
		}
	}

	// "README" matches only the readme.
	if got, _ := search("repos", "README"); !has(got, "repos/myrepo/README.md") {
		t.Errorf("want README.md in %v", got)
	}

	// denylist: searching home root must never surface .codex/index.ts.
	if got, _ := search("", "index"); has(got, ".codex/index.ts") {
		t.Errorf("denylisted path surfaced: %v", got)
	}

	// empty query → empty results, no error.
	if got, _ := search("repos", ""); len(got) != 0 {
		t.Errorf("empty query should yield no results, got %v", got)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
}
func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
