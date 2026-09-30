package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
)

// TestGitflowInitRoutes drives Initialize Git Flow over a temp clone: the opening state,
// the refusal of a missing development branch, the write, the 409 of a stale dialog, and the
// work-item launch's suggestion turning into a declaration.
func TestGitflowInitRoutes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(home, "repos", "app")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	git("remote", "add", "origin", "https://github.com/acme/app.git")
	git("update-ref", "refs/remotes/origin/main", "HEAD")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{name}/gitflow", handleGetGitflow)
	mux.HandleFunc("POST /repos/{name}/gitflow/init", handleGitflowInit)
	mux.HandleFunc("POST /repos/{name}/branch-name", handleBranchName)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var st branchrule.GitflowState
	do(t, srv, "GET", "/repos/app/gitflow", nil, http.StatusOK, &st)
	if st.Prefill.Production != "main" || st.Prefill.Development != "develop" || len(st.Current) != 0 {
		t.Fatalf("state = %+v", st)
	}

	var e gitflowErrOut
	body := gitflowInitRequest{Expected: st.Current, Values: st.Prefill}
	do(t, srv, "POST", "/repos/app/gitflow/init", body, http.StatusBadRequest, &e)
	if e.Error.Code != "branch_missing" || e.Error.Field != "development" {
		t.Fatalf("error = %+v", e)
	}

	git("update-ref", "refs/remotes/origin/develop", "HEAD")
	var named struct {
		Gitflow string `json:"gitflow"`
		Base    string `json:"base"`
	}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{"item": map[string]any{"provider": "github", "key": "acme/app#1", "title": "x"}}, http.StatusOK, &named)
	if named.Gitflow != "suggest" || named.Base != "head" {
		t.Fatalf("before init: %+v", named)
	}

	var out gitflowInitOut
	do(t, srv, "POST", "/repos/app/gitflow/init", body, http.StatusOK, &out)
	if len(out.Written) != 7 || out.State.Current["gitflow.branch.develop"] != "develop" {
		t.Fatalf("init = %+v", out)
	}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{"item": map[string]any{"provider": "github", "key": "acme/app#1", "title": "x"}}, http.StatusOK, &named)
	if named.Gitflow != "declared" || named.Base != "develop" {
		t.Errorf("after init: %+v", named)
	}

	// The same dialog pressed again carries the keys it opened with, which are gone now.
	do(t, srv, "POST", "/repos/app/gitflow/init", body, http.StatusConflict, &e)
	if e.Error.Code != "gitflow_changed" {
		t.Errorf("error = %+v", e)
	}
	bad := gitflowInitRequest{Expected: out.State.Current, Values: out.State.Prefill}
	bad.Values.Feature = "a..b/"
	do(t, srv, "POST", "/repos/app/gitflow/init", bad, http.StatusBadRequest, &e)
	if e.Error.Code != "invalid_value" || e.Error.Field != "feature" {
		t.Errorf("error = %+v", e)
	}
	if code := httpStatus(t, srv, "GET", "/repos/nope/gitflow", nil); code != http.StatusNotFound {
		t.Errorf("unknown repo = %d", code)
	}
}
