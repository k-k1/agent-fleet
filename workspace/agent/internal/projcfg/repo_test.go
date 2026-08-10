package projcfg

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	return dir
}

func TestDetectVCS(t *testing.T) {
	if got := DetectVCS(newGitRepo(t)); got != VCSGit {
		t.Fatalf("git repo: got %q", got)
	}
	svnDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(svnDir, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DetectVCS(svnDir); got != VCSSVN {
		t.Fatalf("svn repo: got %q", got)
	}
	if got := DetectVCS(t.TempDir()); got != VCSNone {
		t.Fatalf("plain dir: got %q", got)
	}
}

func TestTrackTrackedAndIgnored(t *testing.T) {
	dir := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "tracked.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "tracked.json")
	runGit(t, dir, "commit", "-q", "-m", "x")
	if err := os.WriteFile(filepath.Join(dir, "untracked.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := Track(dir, VCSGit, "tracked.json")
	if !st.Tracked || st.Ignored || st.Uncertain {
		t.Fatalf("tracked.json: %+v", st)
	}
	st = Track(dir, VCSGit, "untracked.json")
	if st.Tracked || st.Ignored || st.Uncertain {
		t.Fatalf("untracked.json: %+v", st)
	}
	st = Track(dir, VCSGit, "ignored.json")
	if st.Tracked || !st.Ignored || st.Uncertain {
		t.Fatalf("ignored.json: %+v", st)
	}
	st = Track(dir, VCSGit, "missing.json")
	if st.Tracked || st.Ignored || st.Uncertain {
		t.Fatalf("missing.json: %+v", st)
	}
}

func TestTrackUncertainOutsideGit(t *testing.T) {
	for _, vcs := range []string{VCSSVN, VCSNone} {
		st := Track(t.TempDir(), vcs, "whatever.json")
		if !st.Uncertain || st.Tracked || st.Ignored {
			t.Fatalf("vcs=%s: %+v", vcs, st)
		}
	}
}

func TestIsWorktree(t *testing.T) {
	parent := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(parent, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "a")
	runGit(t, parent, "commit", "-q", "-m", "x")

	if IsWorktree(parent) {
		t.Fatalf("main clone reported as worktree")
	}

	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit(t, parent, "worktree", "add", "-q", "-b", "side", wtDir)
	if !IsWorktree(wtDir) {
		t.Fatalf("linked worktree not detected")
	}
}
