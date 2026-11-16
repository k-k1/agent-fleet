package memorelay

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// reposHome gives the test its own ~/repos holding a real clone `app` with two worktrees:
// `app@one` (of app) and `app@two` (of app@one, a worktree of a worktree).
func reposHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "repos")
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, app, "init", "-b", "main")
	git(t, app, "commit", "--allow-empty", "-m", "init")
	git(t, app, "worktree", "add", "-b", "one", filepath.Join(root, "app@one"))
	git(t, filepath.Join(root, "app@one"), "worktree", "add", "-b", "two", filepath.Join(root, "app@two"))
	return root
}

// A bare main working copy has no ".git" under it; its worktrees still belong to it.
func TestCreateFilesAWorktreeOfABareMain(t *testing.T) {
	root := reposHome(t)
	bare := filepath.Join(root, "lib")
	src := filepath.Join(root, "app")
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v %s", err, out)
	}
	git(t, bare, "worktree", "add", filepath.Join(root, "lib@one"), "main")
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "lib@one"); got != "lib" {
		t.Errorf("a worktree of a bare main was relayed as %q, want lib", got)
	}
}

// The Agent's own GIT_DIR must not change which repository the folder is judged in.
func TestCreateIgnoresGitDirFromTheEnvironment(t *testing.T) {
	root := reposHome(t)
	t.Setenv("GIT_DIR", filepath.Join(root, "app", ".git"))
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "app@one"); got != "app" {
		t.Errorf("with GIT_DIR set the worktree was relayed as %q, want app", got)
	}
}

// An "@" folder that is no git repository at all is left alone too.
func TestCreateLeavesAPlainFolderThatIsNotARepository(t *testing.T) {
	root := reposHome(t)
	if err := os.MkdirAll(filepath.Join(root, "notes@x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "notes@x"); got != "notes@x" {
		t.Errorf("a non-repository folder was relayed as %q", got)
	}
}

func createdRepo(t *testing.T, cp *fakeCP, repo string) string {
	t.Helper()
	cp.bodies = nil
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"memo":{"kind":"text","body":"x","repo":`+quote(repo)+`}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create(%q) answered %d %s", repo, rec.Code, rec.Body.String())
	}
	if len(cp.bodies) != 1 {
		t.Fatalf("create(%q) made %d CP calls", repo, len(cp.bodies))
	}
	return decode(t, cp.bodies[0])["repo"].(string)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestCreateFilesAWorktreeUnderItsBase(t *testing.T) {
	reposHome(t)
	cp := newFakeCP(t)
	for _, c := range []struct{ in, want string }{
		{"app@one", "app"},
		{"app@two", "app"}, // a worktree of a worktree still ends at the main working copy
		{"app", "app"},
		{"", ""},
	} {
		if got := createdRepo(t, cp, c.in); got != c.want {
			t.Errorf("create repo=%q relayed %q, want %q", c.in, got, c.want)
		}
	}
}

// A folder with "@" in its name that is not a linked worktree is a repository of its own.
func TestCreateLeavesAnAtFolderThatIsNotAWorktree(t *testing.T) {
	root := reposHome(t)
	plain := filepath.Join(root, "tool@v2")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, plain, "init", "-b", "main")
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "tool@v2"); got != "tool@v2" {
		t.Errorf("a plain clone named tool@v2 was relayed as %q", got)
	}
}

// With the folder gone git cannot answer; the Console's name rule does.
func TestCreateUsesTheNameRuleForAGoneFolder(t *testing.T) {
	reposHome(t)
	cp := newFakeCP(t)
	for in, want := range map[string]string{"gone@feat": "gone", "gone@a@b": "gone", "nowhere": "nowhere"} {
		if got := createdRepo(t, cp, in); got != want {
			t.Errorf("create repo=%q relayed %q, want %q", in, got, want)
		}
	}
}

// repo is model input: a name that is not one folder under ~/repos is never joined to a path,
// so not even the name rule touches it. The CP judges it.
func TestCreateNeverTouchesTheFilesystemForAnUnsafeName(t *testing.T) {
	root := reposHome(t)
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, outside, "init", "-b", "main")
	cp := newFakeCP(t)
	for _, in := range []string{"../outside", "app/../app@one", "a/b@c", "..", ".hidden@x", "app@one/", "/etc", "-x@y"} {
		if got := createdRepo(t, cp, in); got != in {
			t.Errorf("unsafe repo %q was relayed as %q, want it unchanged", in, got)
		}
	}
}

// A git call that fails must not fail the memo, and must not guess a repository.
func TestCreateSurvivesAFailingGit(t *testing.T) {
	root := reposHome(t)
	broken := filepath.Join(root, "bad@wt")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "bad@wt"); got != "bad@wt" {
		t.Errorf("a folder git cannot read was relayed as %q, want it unchanged", got)
	}
	t.Setenv("PATH", t.TempDir()) // no git at all
	if got := createdRepo(t, cp, "app@one"); got != "app@one" {
		t.Errorf("with no git the memo was relayed as %q, want it unchanged", got)
	}
}

func TestGitTimeoutDoesNotFailTheMemo(t *testing.T) {
	reposHome(t)
	old := gitTimeout
	gitTimeout = time.Nanosecond
	t.Cleanup(func() { gitTimeout = old })
	cp := newFakeCP(t)
	if got := createdRepo(t, cp, "app@one"); got != "app@one" {
		t.Errorf("a timed-out git relayed %q, want the name unchanged", got)
	}
}

func TestUpdateMapsRepoOnlyWhenSet(t *testing.T) {
	reposHome(t)
	cp := newFakeCP(t)
	rec := serve(HandleUpdate, http.MethodPatch, "/memos/m1", `{"repo":"app@one"}`)
	if rec.Code != http.StatusOK || cp.bodies[0] != `{"repo":"app"}` {
		t.Errorf("update with repo relayed %s (status %d)", cp.bodies[0], rec.Code)
	}
	cp.bodies = nil
	serve(HandleUpdate, http.MethodPatch, "/memos/m1", `{"body":"b"}`)
	if cp.bodies[0] != `{"body":"b"}` {
		t.Errorf("update without repo relayed %s, want no repo", cp.bodies[0])
	}
	cp.bodies = nil
	serve(HandleUpdate, http.MethodPatch, "/memos/m1", `{"repo":""}`)
	if cp.bodies[0] != `{"repo":""}` {
		t.Errorf("update unfiling relayed %s", cp.bodies[0])
	}
}
