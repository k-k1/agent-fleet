package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathWithinCwd(t *testing.T) {
	cwd := t.TempDir()
	full, err := resolvePath(cwd, "a/b.txt")
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	want := filepath.Join(cwd, "a", "b.txt")
	if full != want {
		t.Fatalf("full = %q, want %q", full, want)
	}
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	cwd := t.TempDir()
	for _, p := range []string{"../outside.txt", "a/../../outside.txt", ".."} {
		if _, err := resolvePath(cwd, p); err == nil {
			t.Errorf("resolvePath(%q) accepted a path outside cwd", p)
		}
	}
}

func TestResolvePathRejectsAbsoluteOutsideCwd(t *testing.T) {
	cwd := t.TempDir()
	if _, err := resolvePath(cwd, "/etc/passwd"); err == nil {
		t.Fatal("resolvePath accepted an absolute path outside cwd")
	}
}

func TestResolvePathAllowsAbsoluteInsideCwd(t *testing.T) {
	cwd := t.TempDir()
	abs := filepath.Join(cwd, "x", "y.txt")
	full, err := resolvePath(cwd, abs)
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if full != abs {
		t.Fatalf("full = %q, want %q", full, abs)
	}
}

// TestResolvePathRejectsSymlinkEscape is the case a purely lexical containment
// check (path.Clean + strings.HasPrefix) cannot catch: a symlink planted INSIDE
// cwd whose target is outside it. This is the property ADR 0093 decision 5 exists
// to guarantee (a session's tools stay inside its own working directory), so it is
// asserted directly rather than only through pathguard's own tests.
func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("shh"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := resolvePath(cwd, "escape/secret.txt"); err == nil {
		t.Fatal("resolvePath followed a symlink out of cwd")
	}
}
