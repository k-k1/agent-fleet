package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		// SSH vs HTTPS, .git suffix, trailing slash, case — all "same repo".
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo", true},
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo/", true},
		{"https://GitHub.com/Owner/Repo", "https://github.com/owner/repo", true},
		// Different owner / different repo => not the same.
		{"https://github.com/alice/app", "https://github.com/bob/app", false},
		{"https://github.com/owner/a", "https://github.com/owner/b", false},
	}
	for _, c := range cases {
		if got := normalizeRemote(c.a) == normalizeRemote(c.b); got != c.same {
			t.Errorf("normalizeRemote(%q)==normalizeRemote(%q) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

// gitInit creates a local repo at dir with one commit on main and a "feature"
// branch, usable as a clone source (file:// path).
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")
	run("branch", "feature")
}

func TestEnsureRepoSideBySideBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := filepath.Join(t.TempDir(), "app")
	gitInit(t, src)
	remote := "file://" + src

	// Default branch into the legacy bare-name folder.
	dirMain, err := EnsureRepo(remote, "main", "", "")
	if err != nil {
		t.Fatalf("ensureRepo main: %v", err)
	}
	if want := filepath.Join(home, "repos", "app"); dirMain != want {
		t.Fatalf("main dir = %q, want %q", dirMain, want)
	}

	// Same repo, different branch, explicit distinct name => a separate clone.
	dirFeat, err := EnsureRepo(remote, "feature", "", "app-feature")
	if err != nil {
		t.Fatalf("ensureRepo feature: %v", err)
	}
	if dirFeat == dirMain {
		t.Fatalf("feature reused the main clone (%q); want a separate dir", dirFeat)
	}
	if !IsGitRepo(dirMain) || !IsGitRepo(dirFeat) {
		t.Fatalf("both working copies should exist: main=%v feature=%v", IsGitRepo(dirMain), IsGitRepo(dirFeat))
	}
	if br, _ := GitStatus(dirFeat); br.Branch != "feature" {
		t.Fatalf("feature clone branch = %q, want feature", br.Branch)
	}
	if br, _ := GitStatus(dirMain); br.Branch != "main" {
		t.Fatalf("main clone branch = %q, want main (should be untouched)", br.Branch)
	}
}

func TestEnsureRepoNewBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := filepath.Join(t.TempDir(), "app")
	gitInit(t, src)
	remote := "file://" + src

	// Clone at base "main" and fork a fresh branch, into its own folder.
	dir, err := EnsureRepo(remote, "main", "feat/x", "app-feat-x")
	if err != nil {
		t.Fatalf("ensureRepo new branch: %v", err)
	}
	if want := filepath.Join(home, "repos", "app-feat-x"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if br, _ := GitStatus(dir); br.Branch != "feat/x" {
		t.Fatalf("branch = %q, want feat/x", br.Branch)
	}

	// Reusing the same folder + new branch simply switches back to it (no error even
	// though the branch already exists).
	dir2, err := EnsureRepo(remote, "main", "feat/x", "app-feat-x")
	if err != nil {
		t.Fatalf("ensureRepo reuse new branch: %v", err)
	}
	if dir2 != dir {
		t.Fatalf("reuse dir = %q, want %q", dir2, dir)
	}
	if br, _ := GitStatus(dir2); br.Branch != "feat/x" {
		t.Fatalf("reuse branch = %q, want feat/x", br.Branch)
	}
}

// TestEnsureRepoEmptyRemote covers cloning a freshly created, commit-less remote
// (what the internal git provider makes): `git clone --branch main` fails against
// a repo with zero refs, so gitClone must fall back to a plain clone that lands on
// the unborn default branch, and a reused copy must not try to check the unborn
// branch out. A missing branch on a POPULATED remote must still fail loudly.
func TestEnsureRepoEmptyRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := filepath.Join(t.TempDir(), "app.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", src).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	remote := "file://" + src

	// Clone at the (not-yet-born) default branch => fallback plain clone on unborn main.
	dir, err := EnsureRepo(remote, "main", "", "")
	if err != nil {
		t.Fatalf("ensureRepo empty remote: %v", err)
	}
	if got := unbornHead(dir); got != "main" {
		t.Fatalf("unborn HEAD = %q, want main", got)
	}

	// Reuse the same copy at the same branch: must not attempt a checkout of a
	// ref that does not exist yet.
	if _, err := EnsureRepo(remote, "main", "", ""); err != nil {
		t.Fatalf("ensureRepo reuse empty clone: %v", err)
	}

	// Fork a session branch off the unborn base (checkout -b works on unborn HEAD).
	dirNB, err := EnsureRepo(remote, "main", "temp/x", "app-x")
	if err != nil {
		t.Fatalf("ensureRepo empty remote + new branch: %v", err)
	}
	if got := unbornHead(dirNB); got != "temp/x" {
		t.Fatalf("unborn HEAD after fork = %q, want temp/x", got)
	}

	// A populated remote with a missing branch must NOT silently fall back.
	srcFull := filepath.Join(t.TempDir(), "full")
	gitInit(t, srcFull)
	if _, err := EnsureRepo("file://"+srcFull, "nosuch", "", "full-nosuch"); err == nil {
		t.Fatalf("ensureRepo populated remote + missing branch: expected error, got nil")
	}
}

func TestEnsureRepoOriginMismatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Two distinct remotes whose last path segment collides on "app".
	srcA := filepath.Join(t.TempDir(), "alice", "app")
	srcB := filepath.Join(t.TempDir(), "bob", "app")
	gitInit(t, srcA)
	gitInit(t, srcB)

	if _, err := EnsureRepo("file://"+srcA, "main", "", ""); err != nil {
		t.Fatalf("ensureRepo A: %v", err)
	}
	// Same derived name "app", different remote => must refuse, not silently reuse.
	if _, err := EnsureRepo("file://"+srcB, "main", "", ""); err == nil {
		t.Fatalf("ensureRepo B: expected origin-mismatch error, got nil (silent reuse)")
	}
	// With a distinct name it succeeds as an independent clone.
	if _, err := EnsureRepo("file://"+srcB, "main", "", "app-bob"); err != nil {
		t.Fatalf("ensureRepo B with distinct name: %v", err)
	}
}
