package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// scratchShim puts the REAL af-scratch.sh on PATH under the name the agent calls,
// so these tests exercise the shipped script rather than a stand-in of it.
func scratchShim(t *testing.T) {
	t.Helper()
	script, err := filepath.Abs("../af-scratch.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("af-scratch.sh not found: %v", err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nexec bash " + script + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "af-scratch"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newTestRepo(t *testing.T, ignore string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if ignore != "" {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(ignore), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A fresh clone has no node_modules yet — the case the whole feature exists for.
// The symlink must be in place BEFORE anything installs, or the first npm ci runs
// on EFS and the 105s→11s difference is lost (docs/63 §63.5).
func TestScratchAutoRelocateCreatesLinkForAbsentDir(t *testing.T) {
	scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repo := newTestRepo(t, "node_modules/\n")

	scratchAutoRelocate(repo)

	link := filepath.Join(repo, "node_modules")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("node_modules is not a symlink: %v", err)
	}
	if !filepath.IsAbs(target) || !isUnder(target, scratch) {
		t.Errorf("node_modules -> %s, want a path under %s", target, scratch)
	}
	if st, err := os.Stat(link); err != nil || !st.IsDir() {
		t.Errorf("symlink does not resolve to a directory: %v", err)
	}
}

// Anything git does not ignore may be tracked content. Moving it would look like a
// deletion in the working copy, so the script must leave it alone.
func TestScratchAutoRelocateLeavesNonIgnoredDir(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "") // no .gitignore → node_modules is not ignored
	real := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "tracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratchAutoRelocate(repo)

	if _, err := os.Readlink(real); err == nil {
		t.Fatal("a non-ignored node_modules was relocated; tracked content must never move")
	}
	if _, err := os.Stat(filepath.Join(real, "tracked.txt")); err != nil {
		t.Fatalf("content disappeared: %v", err)
	}
}

// An ignored directory that already exists is regenerable, so it moves — and the
// contents must survive the move (the caches inside are the point).
func TestScratchAutoRelocateMovesIgnoredDir(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "node_modules/\n")
	real := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "keep.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratchAutoRelocate(repo)

	if _, err := os.Readlink(real); err != nil {
		t.Fatalf("ignored node_modules was not relocated: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(real, "keep.txt"))
	if err != nil || string(b) != "kept" {
		t.Fatalf("content lost across the move: %q %v", b, err)
	}
}

// A symlink is either ours (idempotent re-run) or one the user pointed at a shared
// tree; both must survive untouched.
func TestScratchAutoRelocateIsIdempotent(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "node_modules/\n")

	scratchAutoRelocate(repo)
	first, err := os.Readlink(filepath.Join(repo, "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	scratchAutoRelocate(repo)
	second, err := os.Readlink(filepath.Join(repo, "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("second run re-pointed the link: %s -> %s", first, second)
	}
}

// Without a working disk (docker / native, or an ECS deployment whose disk is too
// small) the agent must not even fork the helper — clone happens on every runtime.
func TestScratchAutoRelocateSkippedWithoutWorkingDisk(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "ran")
	shim := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(bin, "af-scratch"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_WS_SCRATCH", "")

	scratchAutoRelocate(t.TempDir())

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("af-scratch was invoked although no working disk is configured")
	}
}

func isUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}
