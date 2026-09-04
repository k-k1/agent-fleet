package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// postInitRepo drives the real POST /repos/init handler.
func postInitRepo(t *testing.T, name string) (int, gitx.Repo, string) {
	t.Helper()
	body := strings.NewReader(`{"name":` + strconv.Quote(name) + `}`)
	rec := httptest.NewRecorder()
	gitx.HandleInitRepo(rec, httptest.NewRequest("POST", "/repos/init", body))
	var env struct {
		Repo  gitx.Repo `json:"repo"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v: %s", err, rec.Body.String())
	}
	return rec.Code, env.Repo, env.Error.Code
}

// The backbone of starting where there is nothing to import from: create the folder, run
// git init, and appear in GET /repos. Miss that last step and the working copy is on disk but
// absent from the left pane — a session can still be launched in it, yet it gets no row, no
// diff and no delete.
func TestInitRepoCreatesListedWorkingCopy(t *testing.T) {
	resetRepoJobs(t)
	t.Setenv("HOME", t.TempDir())

	code, repo, errCode := postInitRepo(t, "new-project")
	if code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", code, errCode, http.StatusCreated)
	}
	dir := filepath.Join(gitx.ReposRoot(), "new-project")
	if repo.Path != dir {
		t.Errorf("path = %q, want %q", repo.Path, dir)
	}
	if !gitx.IsGitRepo(dir) {
		t.Fatalf("%s is not a git repository after init", dir)
	}
	// No commits means unborn. A worktree cannot be created here, so the UI needs a flag that
	// stops it offering the option (`git worktree add` fails "not a valid object name: HEAD").
	if !repo.Unborn {
		t.Error("unborn = false on a freshly initialized repository")
	}
	if repo.Branch == "" {
		t.Error("branch is empty; the init branch name should be reported")
	}

	rec := httptest.NewRecorder()
	gitx.HandleListRepos(rec, httptest.NewRequest("GET", "/repos", nil))
	var env struct {
		Repos []gitx.Repo `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	if len(env.Repos) != 1 || env.Repos[0].Name != "new-project" {
		t.Fatalf("GET /repos = %+v, want the new working copy", env.Repos)
	}
	if !env.Repos[0].Unborn {
		t.Error("GET /repos reported unborn = false; gitx.GitStatus must read # branch.oid (initial)")
	}
	if env.Repos[0].Branch != repo.Branch {
		t.Errorf("branch drifted between init (%q) and list (%q)", repo.Branch, env.Repos[0].Branch)
	}
}

// "No history yet" is not a failure. `git log` treats an unborn repository as fatal, so
// passing that through makes the history view error out on every freshly created working copy
// (the graph side already answers with an empty list, because `git log --all` returns 0).
func TestRepoLogOnUnbornRepoIsEmptyNotError(t *testing.T) {
	resetRepoJobs(t)
	t.Setenv("HOME", t.TempDir())
	if code, _, errCode := postInitRepo(t, "fresh"); code != http.StatusCreated {
		t.Fatalf("init failed: %d %s", code, errCode)
	}

	req := httptest.NewRequest("GET", "/repos/fresh/log", nil)
	req.SetPathValue("name", "fresh")
	rec := httptest.NewRecorder()
	gitx.HandleRepoLog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Commits []struct{} `json:"commits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if len(env.Commits) != 0 {
		t.Errorf("commits = %d, want 0", len(env.Commits))
	}
}

// An existing folder must never be clobbered silently; it is refused with the same 409 exists
// a clone uses.
func TestInitRepoRefusesExistingFolder(t *testing.T) {
	resetRepoJobs(t)
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(gitx.ReposRoot(), "taken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("existing work"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errCode := postInitRepo(t, "taken")
	if code != http.StatusConflict || errCode != "exists" {
		t.Fatalf("status = %d code = %q, want 409/exists", code, errCode)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "existing work" {
		t.Fatalf("the existing folder was touched: %v / %q", err, b)
	}
	if gitx.IsGitRepo(dir) {
		t.Error("git init ran inside the existing folder")
	}
}

// Names are rejected by repoNameRe, which doubles as the path-traversal guard.
func TestInitRepoRejectsBadName(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, name := range []string{"", "../escape", "a/b", ".hidden"} {
		code, _, errCode := postInitRepo(t, name)
		if code != http.StatusBadRequest || errCode != "bad_name" {
			t.Errorf("name %q: status = %d code = %q, want 400/bad_name", name, code, errCode)
		}
	}
	// Nothing must have been created; reposRoot itself being absent is the normal case.
	if entries, err := os.ReadDir(gitx.ReposRoot()); err == nil && len(entries) > 0 {
		t.Errorf("a rejected name still created %d entries under ~/repos", len(entries))
	}
}
