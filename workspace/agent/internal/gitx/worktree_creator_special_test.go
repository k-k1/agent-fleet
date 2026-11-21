//go:build unix

package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A marker replaced by a named pipe with no writer must read as unreadable, not block the
// caller (the trash delete reads it under the deletion gate).
func TestWorktreeCreatorSpecialFileDoesNotBlock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt, _, err := EnsureWorktreeNew(parent, "main", "feat-a", "")
	if err != nil {
		t.Fatal(err)
	}
	p := worktreeCreatorPath(wt)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		_, st := ReadWorktreeCreatorState(wt)
		done <- st
	}()
	select {
	case st := <-done:
		if st != CreatorStateUnreadable {
			t.Fatalf("state = %q, want unreadable", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a pipe marker blocked")
	}
	tomb, ok, err := PrepareTombstone(wt, parent)
	if err != nil || !ok || tomb.Creator != "-" {
		t.Fatalf("tombstone ok=%v err=%v creator=%q", ok, err, tomb.Creator)
	}
}
