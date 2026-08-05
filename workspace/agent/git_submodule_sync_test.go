package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSubmoduleStatus(t *testing.T) {
	// The state byte is glued to the sha, and a checked-out entry carries a trailing
	// describe in parentheses — both are what git actually prints.
	out := " 5d126e221e41830478d040112a4a4174fc78d691 libs/ok (remotes/origin/main)\n" +
		"-728b394cc83271b2e27540e69c09c7ae7ef535b9 libs/never-init\n" +
		"+aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa libs/moved (v1.2-3-gabc)\n" +
		"Ubbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb libs/conflicted\n"
	got := parseSubmoduleStatus(out)
	want := []submoduleEntry{
		{Path: "libs/ok", SHA: "5d126e221e41830478d040112a4a4174fc78d691", State: ' '},
		{Path: "libs/never-init", SHA: "728b394cc83271b2e27540e69c09c7ae7ef535b9", State: '-'},
		{Path: "libs/moved", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: '+'},
		{Path: "libs/conflicted", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", State: 'U'},
	}
	if len(got) != len(want) {
		t.Fatalf("parseSubmoduleStatus returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSubmodulePathEmpty(t *testing.T) {
	dir := t.TempDir()
	if !submodulePathEmpty(filepath.Join(dir, "nope")) {
		t.Error("an absent submodule directory must count as empty")
	}
	wedged := filepath.Join(dir, "wedged")
	if err := os.MkdirAll(wedged, 0o755); err != nil {
		t.Fatal(err)
	}
	// The wedge a killed clone leaves: the gitfile is there, the content is not.
	if err := os.WriteFile(filepath.Join(wedged, ".git"), []byte("gitdir: ../x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !submodulePathEmpty(wedged) {
		t.Error("a submodule holding only .git is the wedged state and must count as empty")
	}
	if err := os.WriteFile(filepath.Join(wedged, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if submodulePathEmpty(wedged) {
		t.Error("a submodule with a tracked file must NOT count as empty")
	}
}

// TestSubmoduleWedgeRepair is the regression for the reported "the submodules are broken"
// launch. It pins both halves of the finding: git cannot get out of the wedge on its own
// (`submodule update` fails forever, so a relaunch alone never helps), and repairSubmodules
// does — and that submoduleGaps sees the wedge at all, which git's own status does not.
func TestSubmoduleWedgeRepair(t *testing.T) {
	git := gitTestEnv(t)
	wt, smDir := wedgedWorktree(t, git)

	gaps := submoduleGaps(wt)
	if len(gaps) != 1 || gaps[0].Path != "libs/sub" {
		t.Fatalf("submoduleGaps = %+v, want the wedged libs/sub — git's own status calls it healthy, "+
			"so the empty working tree is the only signal", gaps)
	}

	if out, err := exec.Command("git", "-C", wt, "-c", "protocol.file.allow=always",
		"submodule", "update", "--init", "--recursive").CombinedOutput(); err == nil {
		t.Fatalf("`submodule update` unexpectedly repaired the wedge: %s\n"+
			"if a newer git resumes on its own, repairSubmodules can be reconsidered", out)
	}

	if left := repairSubmodules(wt, gaps); len(left) != 0 {
		t.Fatalf("repairSubmodules left %+v behind", left)
	}
	if submodulePathEmpty(smDir) {
		t.Error("after the repair the submodule working tree is still empty")
	}
	if got := len(submoduleGaps(wt)); got != 0 {
		t.Errorf("submoduleGaps after repair = %d, want 0", got)
	}
}

// TestGitSubmodulesEnsureRepairs covers the launch path itself: ensure() must notice the gap
// and heal it, so a relaunch into a wedged worktree stops handing the session an empty
// directory.
func TestGitSubmodulesEnsureRepairs(t *testing.T) {
	git := gitTestEnv(t)
	_, smDir := wedgedWorktree(t, git)

	gitSubmodulesEnsure(filepath.Dir(filepath.Dir(smDir)))
	// The repair runs in a goroutine (its fetch can take minutes against a real remote).
	deadline := time.Now().Add(20 * time.Second)
	for submodulePathEmpty(smDir) {
		if time.Now().After(deadline) {
			t.Fatal("gitSubmodulesEnsure did not repair the wedged submodule")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// wedgedWorktree builds a parent repo with a submodule, adds a worktree with the submodule
// checked out, and then reproduces the state a killed `submodule update` leaves behind: the
// object store stays, the working tree and index are gone, and HEAD points at an unborn
// branch so nothing records which commit is checked out. Returns the worktree and the
// submodule path inside it.
func wedgedWorktree(t *testing.T, git gitRunner) (wt, smDir string) {
	t.Helper()
	root := t.TempDir()
	sub, parent := filepath.Join(root, "sub"), filepath.Join(root, "parent")

	git(t, "", "init", "-q", "-b", "main", sub)
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	git(t, sub, "add", "-A")
	git(t, sub, "commit", "-qm", "init")

	git(t, "", "init", "-q", "-b", "main", parent)
	writeFile(t, filepath.Join(parent, "p.txt"), "p\n")
	git(t, parent, "add", "-A")
	git(t, parent, "commit", "-qm", "init")
	// Local-path submodules need protocol.file.allow (git's CVE-2022-39253 hardening).
	git(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "libs/sub")
	git(t, parent, "commit", "-qm", "add submodule")

	wt = filepath.Join(root, "wt")
	git(t, parent, "worktree", "add", "-q", wt, "-b", "feat")
	git(t, wt, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")

	smDir = filepath.Join(wt, "libs", "sub")
	if submodulePathEmpty(smDir) {
		t.Fatal("setup: the worktree's submodule should be checked out before we wedge it")
	}
	gitDir := strings.TrimSpace(git(t, smDir, "rev-parse", "--absolute-git-dir"))
	if err := os.Remove(filepath.Join(smDir, "lib.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(gitDir, "index")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/master\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return wt, smDir
}

type gitRunner func(t *testing.T, dir string, args ...string) string

// gitTestEnv isolates git from the developer's / CI runner's own configuration (HOME alone is
// not enough — a GitHub runner exports XDG_CONFIG_HOME) and returns a helper that fails the
// test on any git error.
func gitTestEnv(t *testing.T) gitRunner {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_AUTHOR_NAME", "af-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "af-test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "af-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "af-test@example.invalid")
	return func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		if dir != "" {
			args = append([]string{"-C", dir}, args...)
		}
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
}
