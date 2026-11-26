package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A linked worktree holding a directory without owner write (Go's module cache is mode 0555):
// `git worktree remove --force` cannot unlink inside it and leaves the folder behind. The removal
// is finished by hand, so the delete does not fail for good.
func TestRemoveWorktreeThroughAReadOnlyDirectory(t *testing.T) {
	removeReadOnlyWorktree(t, false)
}

// A lock stands even when its free-text reason happens to contain the words git prints for a
// permission failure: the worktree is left whole and still registered.
func TestRemoveWorktreeKeepsALockWhoseReasonMentionsPermissions(t *testing.T) {
	removeReadOnlyWorktree(t, true)
}

func removeReadOnlyWorktree(t *testing.T, locked bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "app")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "init", "-q", "-b", "main")
	gitT(t, parent, "commit", "-q", "--allow-empty", "-m", "i")
	wt := filepath.Join(root, "app@ro")
	gitT(t, parent, "worktree", "add", "-q", "-b", "ro", wt, "main")

	if locked {
		gitT(t, parent, "worktree", "lock", "--reason", "permission denied by owner", wt)
	}
	mod := filepath.Join(wt, "go", "pkg", "mod", "example.com", "m@v1")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "m.go"), []byte("package m"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{mod, filepath.Join(wt, "go", "pkg", "mod")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { // a red run must not leave a tree TempDir's own cleanup cannot remove
		_ = filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	probe := filepath.Join(wt, "go", "pkg", "mod", "probe")
	if err := os.WriteFile(probe, nil, 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("directory permissions are not enforced here (running as root)")
	}

	out, err := removeWorktree(parent, wt)
	list, lerr := exec.Command("git", "-C", parent, "worktree", "list", "--porcelain").Output()
	if lerr != nil {
		t.Fatal(lerr)
	}
	registered := strings.Contains(string(list), wt)
	if locked {
		if err == nil {
			t.Fatal("a locked worktree was removed")
		}
		if _, serr := os.Stat(mod); serr != nil {
			t.Fatalf("a locked worktree lost files: %v", serr)
		}
		if !registered {
			t.Fatalf("the locked worktree lost its registration:\n%s", list)
		}
		return
	}
	if err != nil {
		t.Fatalf("removeWorktree: %v\n%s", err, out)
	}
	if _, serr := os.Lstat(wt); !os.IsNotExist(serr) {
		t.Fatalf("the worktree folder is still there: %v", serr)
	}
	if registered {
		t.Fatalf("the worktree is still registered:\n%s", list)
	}
}
