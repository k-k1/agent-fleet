package rmtree

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
}

// lockDir takes owner write away from dir, the way a module cache leaves its directories.
func lockDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	// Registered before the chmod, so a failing test still lets t.TempDir's own cleanup remove
	// the tree: cleanups run last-in first-out, which opens the innermost directory first.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.Mkdir(probe, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(probe, 0o755)
	if err := os.WriteFile(filepath.Join(probe, "f"), nil, 0o600); err == nil {
		t.Skip("permissions are not enforced here (running as root)")
	}
}

func TestRemoveAllReadOnlyDirectories(t *testing.T) {
	skipIfRoot(t)
	for name, mode := range map[string]os.FileMode{
		"0555 (no write)":  0o555,
		"0500":             0o500,
		"0300 (no read)":   0o300,
		"0000 (no access)": 0o000,
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "trashed")
			mustWrite(t, filepath.Join(root, "pkg", "mod", "a", "f.go"))
			mustWrite(t, filepath.Join(root, "pkg", "mod", "a", "deep", "g.go"))
			mustWrite(t, filepath.Join(root, "top.txt"))
			// Innermost first, so each chmod is still reachable.
			lockDir(t, filepath.Join(root, "pkg", "mod", "a", "deep"), mode)
			lockDir(t, filepath.Join(root, "pkg", "mod", "a"), mode)
			lockDir(t, filepath.Join(root, "pkg"), mode)

			// Plain RemoveAll must fail here, or this test proves nothing.
			if err := os.RemoveAll(root); err == nil {
				t.Skip("os.RemoveAll succeeded on its own; the fixture does not reproduce the failure")
			}
			if err := RemoveAll(root); err != nil {
				t.Fatalf("RemoveAll: %v", err)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatalf("root still there: %v", err)
			}
		})
	}
}

func TestRemoveAllDoesNotFollowSymlinks(t *testing.T) {
	skipIfRoot(t)
	outside := filepath.Join(t.TempDir(), "outside")
	mustWrite(t, filepath.Join(outside, "keep.txt"))
	lockDir(t, outside, 0o555)

	root := filepath.Join(t.TempDir(), "trashed")
	mustWrite(t, filepath.Join(root, "ro", "f"))
	lockDir(t, filepath.Join(root, "ro"), 0o555)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("root still there: %v", err)
	}
	fi, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("the link target was removed: %v", err)
	}
	if fi.Mode().Perm() != 0o555 {
		t.Fatalf("the link target was chmod'ed to %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("a file behind the link was removed: %v", err)
	}
}

func TestRemoveAllMissingAndPlain(t *testing.T) {
	if err := RemoveAll(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("a missing path is not an error: %v", err)
	}
	root := filepath.Join(t.TempDir(), "plain")
	mustWrite(t, filepath.Join(root, "a", "b"))
	if err := RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}

// A directory on another device (a mount) is neither opened up nor emptied, and the final removal
// does not walk into it either: a parent that was unreachable until the walk opened it must not
// make the writable data behind the mount deletable.
func TestRemoveAllStopsAtAMountPoint(t *testing.T) {
	skipIfRoot(t)
	root := filepath.Join(t.TempDir(), "trashed")
	mustWrite(t, filepath.Join(root, "ro", "mounted", "data.txt")) // writable parent: removable if reached
	mustWrite(t, filepath.Join(root, "ro", "plain.txt"))
	mounted := filepath.Join(root, "ro", "mounted")
	lockDir(t, filepath.Join(root, "ro"), 0o000)

	prev := devOf
	devOf = func(fi os.FileInfo) uint64 {
		if fi.Name() == "mounted" {
			return 42
		}
		return prev(fi)
	}
	t.Cleanup(func() { devOf = prev })

	if err := RemoveAll(root); err == nil {
		t.Fatal("RemoveAll went through a mount point")
	}
	if _, err := os.Stat(filepath.Join(mounted, "data.txt")); err != nil {
		t.Fatalf("what lives behind the mount was removed: %v", err)
	}
}
