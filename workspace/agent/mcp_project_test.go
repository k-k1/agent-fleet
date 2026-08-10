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

// TestHandleRepoMCPPlanApply exercises docs/56 P1's plan → apply route pair at
// the HTTP boundary: real buildMux, real route registration (this is exactly the
// "both agent AND control-plane routes.go" contract docs/56 §10 warns about — a
// route missing from one side 404s from the Console, not from a direct agent
// test like this one).
func TestHandleRepoMCPPlanApply(t *testing.T) {
	h := smokeHandler(t)

	repoDir := filepath.Join(homeDir(), "repos", "proj2")
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
	body := `{"mcpServers":{"syosetu":{"type":"stdio","command":"uv","args":["${HOME}/x.py"]}}}`
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".mcp.json")
	run("commit", "-q", "-m", "x")

	opsBody := `{"ops":[{"op":"copy","from":{"file":".mcp.json","name":"syosetu"},
	  "to":{"file":"opencode.json"},"onConflict":"overwrite","dialect":"translate"}]}`

	w := smokeDo(t, h, "POST", "/repos/proj2/mcp/plan", "smoke-token", opsBody)
	if w.Code != http.StatusOK {
		t.Fatalf("plan status %d: %s", w.Code, w.Body.String())
	}
	var plan struct {
		PlanHash string `json:"planHash"`
		Ops      []struct {
			Status string `json:"status"`
			After  struct {
				Args []string `json:"args"`
			} `json:"after"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v: %s", err, w.Body.String())
	}
	if plan.PlanHash == "" || len(plan.Ops) != 1 || plan.Ops[0].Status != "ok" {
		t.Fatalf("plan: %s", w.Body.String())
	}
	if plan.Ops[0].After.Args[0] != "{env:HOME}/x.py" {
		t.Fatalf("expected translated preview, got %+v", plan.Ops[0].After)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("plan must not have written opencode.json")
	}

	applyBody := `{"ops":[{"op":"copy","from":{"file":".mcp.json","name":"syosetu"},
	  "to":{"file":"opencode.json"},"onConflict":"overwrite","dialect":"translate"}],"planHash":"` + plan.PlanHash + `"}`
	w2 := smokeDo(t, h, "POST", "/repos/proj2/mcp/apply", "smoke-token", applyBody)
	if w2.Code != http.StatusOK {
		t.Fatalf("apply status %d: %s", w2.Code, w2.Body.String())
	}
	out, err := os.ReadFile(filepath.Join(repoDir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "{env:HOME}/x.py") {
		t.Fatalf("opencode.json not written correctly:\n%s", out)
	}

	// A second apply with the SAME (now stale) planHash must 409.
	w3 := smokeDo(t, h, "POST", "/repos/proj2/mcp/apply", "smoke-token", applyBody)
	if w3.Code != http.StatusConflict {
		t.Fatalf("expected 409 on a stale plan, got %d: %s", w3.Code, w3.Body.String())
	}
}
