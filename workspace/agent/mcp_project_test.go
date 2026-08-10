package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandleRepoMCPServesSnapshot is the route-table-level smoke test for docs/56
// P0's GET /repos/{name}/mcp: real buildMux, real repoAnyDirFromPath resolution,
// real mcpproj.Inspect over an actual git working copy — the same shape as the
// motivating novel-lab example, checked for the masked-value contract (docs/56
// §7.3) at the HTTP boundary, not just inside internal/mcpproj's own tests.
func TestHandleRepoMCPServesSnapshot(t *testing.T) {
	h := smokeHandler(t)

	repoDir := filepath.Join(homeDir(), "repos", "proj")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	const secret = "af-test-fixture-deadbeefcafefeedx"
	body := `{"mcpServers":{"srv":{"type":"http","url":"https://example.com/mcp","headers":{"Authorization":"Bearer ` + secret + `"}}}}`
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".mcp.json")
	run("commit", "-q", "-m", "x")

	w := smokeDo(t, h, "GET", "/repos/proj/mcp", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if bodyStr := w.Body.String(); strings.Contains(bodyStr, secret) {
		t.Fatalf("secret leaked through the HTTP response: %s", bodyStr)
	}
	var got struct {
		Repo  string `json:"repo"`
		VCS   string `json:"vcs"`
		Files []struct {
			Path    string `json:"path"`
			Exists  bool   `json:"exists"`
			Servers []struct {
				Name    string            `json:"name"`
				Headers map[string]string `json:"headers"`
			} `json:"servers"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	if got.Repo != "proj" || got.VCS != "git" {
		t.Fatalf("got %+v", got)
	}
	found := false
	for _, f := range got.Files {
		if f.Path == ".mcp.json" {
			found = true
			if !f.Exists || len(f.Servers) != 1 || f.Servers[0].Headers["Authorization"] != "***" {
				t.Fatalf(".mcp.json: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf(".mcp.json missing from files: %+v", got.Files)
	}
}

// TestHandleRepoMCPUnknownRepo404s: same not-found contract as the other
// repo-scoped GETs (repoAnyDirFromPath), so the Console gets a normal 404 rather
// than a panic for a name that doesn't exist.
func TestHandleRepoMCPUnknownRepo404s(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "GET", "/repos/does-not-exist/mcp", "smoke-token", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
