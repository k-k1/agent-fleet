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

// 取り込み元が無いところから始める導線の背骨: フォルダを作り、git init まで済ませ、
// GET /repos に**載る**こと。ここが載らないと「ディスクにはあるが左ペインに無い」
// 作業コピーになり、起動はできるのに行も差分も削除も付かない。
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
	// コミットが無い＝unborn。worktree はここでは作れないので、UI がその選択肢を
	// 出さないための旗が要る（`git worktree add` は "not a valid object name: HEAD"）。
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
		t.Error("GET /repos reported unborn = false; gitStatus must read # branch.oid (initial)")
	}
	if env.Repos[0].Branch != repo.Branch {
		t.Errorf("branch drifted between init (%q) and list (%q)", repo.Branch, env.Repos[0].Branch)
	}
}

// 「まだ履歴が無い」は失敗ではない。`git log` は unborn を fatal 扱いにするので、
// そのまま通すと作った直後の作業コピーで履歴ビューが毎回エラーになる（グラフ側は
// `git log --all` が 0 で返るので既に空一覧で応じている）。
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

// 既存フォルダを黙って壊さないこと。クローンと同じ 409 exists で断る。
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

// 名前は repoNameRe（パストラバーサル防御を兼ねる）で弾く。
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
	// 何も作られていないこと（reposRoot ごと存在しないのが正常）。
	if entries, err := os.ReadDir(gitx.ReposRoot()); err == nil && len(entries) > 0 {
		t.Errorf("a rejected name still created %d entries under ~/repos", len(entries))
	}
}
