package gitx

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// autorunClone builds origin (default branch main, plus a branch "other") and a clone in
// $HOME/repos/app. content is committed to main's settings file unless it is "".
func autorunClone(t *testing.T, content string) (home, seed, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	seed = filepath.Join(home, "seed")
	gitInit(t, seed)
	if content != "" {
		writeFile(t, filepath.Join(seed, AutorunFilePath), content)
		gitAt(t, seed, "add", AutorunFilePath)
		gitAt(t, seed, "commit", "-m", "settings")
	}
	gitAt(t, seed, "checkout", "-b", "other")
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # other\n")
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "other settings")
	gitAt(t, seed, "checkout", "main")
	gitAt(t, home, "clone", "--bare", seed, "origin.git")
	dir = filepath.Join(home, "repos", "app")
	gitAt(t, home, "clone", filepath.Join(home, "origin.git"), dir)
	return home, seed, dir
}

func getAutorunFile(t *testing.T) (int, AutorunFile, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/repos/app/autorun-file", nil)
	req.SetPathValue("name", "app")
	rec := httptest.NewRecorder()
	HandleAutorunFile(rec, req)
	var f AutorunFile
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	return rec.Code, f, rec.Body.String()
}

// The file is read from the commit origin's default branch names after a fetch: not the
// working tree, not the checked-out branch, and not a stale or repointed origin/HEAD.
func TestAutorunFileReadsOnlyTheRemoteDefaultBranch(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # main v1\n")
	// What a run in the workspace can do: edit the working tree, commit on its own branch,
	// repoint origin/HEAD at another branch.
	gitAt(t, dir, "checkout", "-b", "work")
	writeFile(t, filepath.Join(dir, AutorunFilePath), "version: 1 # working tree\n")
	gitAt(t, dir, "commit", "-am", "local settings")
	gitAt(t, dir, "remote", "set-head", "origin", "other")
	// main moves on origin after the clone: only a fetch sees it.
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # main v2\n")
	gitAt(t, seed, "commit", "-am", "settings v2")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	want := strings.TrimSpace(gitAt(t, seed, "rev-parse", "main"))

	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !f.Found || f.Content != "version: 1 # main v2\n" || f.Branch != "main" || f.Commit != want || f.Path != AutorunFilePath ||
		f.Remote != filepath.Join(home, "origin.git") {
		t.Fatalf("got %+v, want main's v2 at %s", f, want)
	}
}

// What the clone says after the fetch does not matter: a tracking ref repointed the moment the
// fetch writes it, and a replace ref that swaps the file's blob, both leave the read on the
// commit origin named and its genuine content.
func TestAutorunFileIgnoresRefsTheCloneRewrites(t *testing.T) {
	home, seed, dir := autorunClone(t, "version: 1 # old\n")
	// main moves on origin, so the fetch writes origin/main and the hook runs.
	writeFile(t, filepath.Join(seed, AutorunFilePath), "version: 1 # main\n")
	gitAt(t, seed, "commit", "-am", "settings v2")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	want := strings.TrimSpace(gitAt(t, seed, "rev-parse", "main"))
	other := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/other"))
	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	writeFile(t, hook, "#!/bin/sh\n[ \"$1\" = committed ] || exit 0\n[ -n \"$AF_TEST_REPOINTED\" ] && exit 0\n"+
		"grep -q refs/remotes/origin/main || exit 0\nAF_TEST_REPOINTED=1 git update-ref refs/remotes/origin/main "+other+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	// The replace ref is made ahead for the blob the new commit will bring.
	blob := strings.TrimSpace(gitAt(t, seed, "rev-parse", want+":"+AutorunFilePath))
	fake := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/other:"+AutorunFilePath))
	gitAt(t, dir, "update-ref", "refs/replace/"+blob, fake)

	code, f, body := getAutorunFile(t)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := strings.TrimSpace(gitAt(t, dir, "rev-parse", "origin/main")); got != other {
		t.Fatalf("the hook did not repoint origin/main (%s): the test proves nothing", got)
	}
	if f.Commit != want || f.Content != "version: 1 # main\n" {
		t.Fatalf("got %+v, want origin's %s with its own content", f, want)
	}
}

func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@github.invalid/o/r.git": "https://github.invalid/o/r.git",
		"https://github.invalid/o/r.git":          "https://github.invalid/o/r.git",
		"git@github.invalid:o/r.git":              "git@github.invalid:o/r.git",
		"/srv/git/r.git":                          "/srv/git/r.git",
	} {
		if got := redactRemote(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}

func TestAutorunFileAbsent(t *testing.T) {
	autorunClone(t, "")
	code, f, body := getAutorunFile(t)
	if code != 200 || f.Found || f.Content != "" || f.Commit == "" || f.Branch != "main" {
		t.Fatalf("status %d, %+v (%s): want found=false with the commit read", code, f, body)
	}
}

func TestAutorunFileRefusesASymlinkAndAnOversizedFile(t *testing.T) {
	home, seed, _ := autorunClone(t, "version: 1\n")
	if err := os.Remove(filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../f", filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "symlink")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	if code, _, body := getAutorunFile(t); code != 422 || !strings.Contains(body, "file_not_regular") {
		t.Fatalf("symlink: status %d: %s", code, body)
	}

	if err := os.Remove(filepath.Join(seed, AutorunFilePath)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, AutorunFilePath), strings.Repeat("#", AutorunFileMaxBytes+1))
	gitAt(t, seed, "add", AutorunFilePath)
	gitAt(t, seed, "commit", "-m", "big")
	gitAt(t, seed, "push", filepath.Join(home, "origin.git"), "main")
	if code, _, body := getAutorunFile(t); code != 422 || !strings.Contains(body, "file_too_large") {
		t.Fatalf("oversized: status %d: %s", code, body)
	}
}

func TestAutorunFileWithoutOrigin(t *testing.T) {
	_, _, dir := autorunClone(t, "version: 1\n")
	gitAt(t, dir, "remote", "remove", "origin")
	if code, _, body := getAutorunFile(t); code != 502 || !strings.Contains(body, "no_default_branch") || strings.Contains(body, "fatal") {
		t.Fatalf("status %d: %s", code, body)
	}
}
