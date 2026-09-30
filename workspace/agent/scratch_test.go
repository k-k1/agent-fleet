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
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A fresh clone has no target/ yet — the case the whole feature exists for.
// The symlink must be in place BEFORE anything builds, or the first build runs
// on EFS (docs/log/63 §63.5).
func TestScratchAutoRelocateCreatesLinkForAbsentDir(t *testing.T) {
	scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repo := newTestRepo(t, "target/\n")

	scratchAutoRelocate(repo)

	link := filepath.Join(repo, "target")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("target is not a symlink: %v", err)
	}
	if !filepath.IsAbs(target) || !isUnder(target, scratch) {
		t.Errorf("target -> %s, want a path under %s", target, scratch)
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
	repo := newTestRepo(t, "") // no .gitignore → target is not ignored
	real := filepath.Join(repo, "target")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "tracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratchAutoRelocate(repo)

	if _, err := os.Readlink(real); err == nil {
		t.Fatal("a non-ignored target was relocated; tracked content must never move")
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
	repo := newTestRepo(t, "target/\n")
	real := filepath.Join(repo, "target")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "keep.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratchAutoRelocate(repo)

	if _, err := os.Readlink(real); err != nil {
		t.Fatalf("ignored target was not relocated: %v", err)
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
	repo := newTestRepo(t, "target/\n")

	scratchAutoRelocate(repo)
	first, err := os.Readlink(filepath.Join(repo, "target"))
	if err != nil {
		t.Fatal(err)
	}
	scratchAutoRelocate(repo)
	second, err := os.Readlink(filepath.Join(repo, "target"))
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

// npm replaces a symlinked node_modules with a real directory on install, and
// `python3 -m venv .venv` refuses a symlink, so pre-creating either link only
// breaks things. Both must stay absent even when git ignores them.
func TestScratchAutoRelocateSkipsNodeModulesAndVenv(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "target/\nnode_modules/\n.venv/\n")
	for _, f := range []string{"package.json", "pyproject.toml"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scratchAutoRelocate(repo)

	for _, a := range []string{"node_modules", ".venv"} {
		if _, err := os.Lstat(filepath.Join(repo, a)); err == nil {
			t.Errorf("%s was pre-created; it must be left for the package manager", a)
		}
	}
	if _, err := os.Readlink(filepath.Join(repo, "target")); err != nil {
		t.Errorf("target beside it was not relocated: %v", err)
	}
}

// By hand, node_modules is refused with a non-zero exit rather than linked.
func TestScratchRefusesNodeModulesByHand(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "node_modules/\n")

	cmd := exec.Command("af-scratch", "node_modules")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("af-scratch node_modules exited 0: %s", out)
	}
	if _, err := os.Lstat(filepath.Join(repo, "node_modules")); err == nil {
		t.Fatal("node_modules was created although the command refused it")
	}
}

func isUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}
