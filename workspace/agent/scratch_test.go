package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scratchShim puts the REAL af-scratch.sh on PATH under the name the agent calls,
// so these tests exercise the shipped script rather than a stand-in of it. It also
// points the link registry at a temp dir, which it returns: without that every test
// would append to the developer's own ~/.local/state/af-scratch/links.
func scratchShim(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("AF_SCRATCH_STATE_DIR", state)
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
	return state
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

func runScratch(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("af-scratch", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("af-scratch %v: %v: %s", args, err, out)
	}
	return string(out)
}

// emptyScratch makes the state a stop leaves behind: the links in the working copy
// survive, the scratch disk comes back empty.
func emptyScratch(t *testing.T, scratch string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(scratch, "artifacts")); err != nil {
		t.Fatal(err)
	}
}

func assertResolvesToDir(t *testing.T, link string) {
	t.Helper()
	if st, err := os.Stat(link); err != nil || !st.IsDir() {
		t.Fatalf("%s does not resolve to a directory: %v", link, err)
	}
}

// After a stop every link --auto made dangles, and Gradle fails on one ("Failed to
// create parent directory", #3987). --repair, which the entrypoint runs, must bring
// the target back from the registry alone.
func TestScratchRepairRecreatesDanglingLink(t *testing.T) {
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repo := newTestRepo(t, "target/\n")
	scratchAutoRelocate(repo)
	link := filepath.Join(repo, "target")

	reg, err := os.ReadFile(filepath.Join(state, "links"))
	if err != nil || !strings.Contains(string(reg), link+"\n") {
		t.Fatalf("--auto did not record %s in the registry: %q %v", link, reg, err)
	}
	emptyScratch(t, scratch)
	if _, err := os.Stat(link); err == nil {
		t.Fatal("test setup: link still resolves after emptying the scratch disk")
	}

	runScratch(t, "--repair")
	assertResolvesToDir(t, link)
}

// A registry entry whose link points outside the scratch disk (the user replaced ours
// with a share, say) must not be followed: mkdir through it would create directories
// wherever it points.
func TestScratchRepairLeavesLinkOutsideScratch(t *testing.T) {
	state := scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := newTestRepo(t, "target/\n")
	outside := filepath.Join(t.TempDir(), "elsewhere", "target")
	link := filepath.Join(repo, "target")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "links"), []byte(link+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runScratch(t, "--repair")

	if _, err := os.Lstat(filepath.Dir(outside)); err == nil {
		t.Fatalf("--repair created %s through a link that points outside the scratch disk", outside)
	}
}

// Entries whose link no longer exists (a removed worktree) are dropped, so the
// registry does not grow forever.
func TestScratchRepairDropsVanishedLinks(t *testing.T) {
	state := scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	gone := filepath.Join(t.TempDir(), "gone", "target")
	if err := os.WriteFile(filepath.Join(state, "links"), []byte(gone+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runScratch(t, "--repair")

	reg, err := os.ReadFile(filepath.Join(state, "links"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reg), gone) {
		t.Errorf("vanished link kept in the registry: %q", reg)
	}
}

// --auto on an existing working copy (a new worktree beside an old one, a re-run)
// revives a dangling link of ours, and still leaves a dangling link of the user's
// alone.
func TestScratchAutoRelocateRevivesOnlyOwnDanglingLink(t *testing.T) {
	scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repo := newTestRepo(t, "target/\nbuild/\n")
	if err := os.WriteFile(filepath.Join(repo, "build.gradle"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scratchAutoRelocate(repo)
	emptyScratch(t, scratch)
	outside := filepath.Join(t.TempDir(), "elsewhere", "build")
	build := filepath.Join(repo, "build")
	if err := os.Remove(build); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, build); err != nil {
		t.Fatal(err)
	}

	scratchAutoRelocate(repo)

	assertResolvesToDir(t, filepath.Join(repo, "target"))
	if _, err := os.Lstat(filepath.Dir(outside)); err == nil {
		t.Fatalf("--auto created %s through a link that points outside the scratch disk", outside)
	}
}

// By hand, `af-scratch target` on a dangling link of ours recreates the target
// instead of reporting "already a symlink" and leaving the build broken.
func TestScratchByHandRevivesDanglingLink(t *testing.T) {
	scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repo := newTestRepo(t, "target/\n")
	cmd := exec.Command("af-scratch", "target")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("af-scratch target: %v: %s", err, out)
	}
	emptyScratch(t, scratch)

	cmd = exec.Command("af-scratch", "target")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("af-scratch target (re-run): %v: %s", err, out)
	}
	assertResolvesToDir(t, filepath.Join(repo, "target"))
}

// Links made before the registry existed are found once by --seed, bounded to the
// depth --auto uses; a link pointing outside the scratch disk is neither recorded nor
// followed, and the marker stops the walk from running again.
func TestScratchSeedFindsPreRegistryLinks(t *testing.T) {
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repos := t.TempDir()
	t.Setenv("AF_SCRATCH_REPOS", repos)
	ours := filepath.Join(repos, "app", "svc", "target")
	theirs := filepath.Join(repos, "app", "build")
	outside := filepath.Join(t.TempDir(), "elsewhere", "build")
	for _, l := range []struct{ link, to string }{
		{ours, filepath.Join(scratch, "artifacts", ours)},
		{theirs, outside},
	} {
		if err := os.MkdirAll(filepath.Dir(l.link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(l.to, l.link); err != nil {
			t.Fatal(err)
		}
	}

	runScratch(t, "--seed")

	assertResolvesToDir(t, ours)
	if _, err := os.Lstat(filepath.Dir(outside)); err == nil {
		t.Fatalf("--seed created %s through a link outside the scratch disk", outside)
	}
	reg, _ := os.ReadFile(filepath.Join(state, "links"))
	if !strings.Contains(string(reg), ours+"\n") || strings.Contains(string(reg), theirs) {
		t.Errorf("registry after --seed = %q, want only %s", reg, ours)
	}
	if _, err := os.Stat(filepath.Join(state, "seeded")); err != nil {
		t.Errorf("--seed left no marker: %v", err)
	}
}

// Both start-time modes run from the entrypoint, which has `set -e` and runs on
// runtimes without a working disk too: there they must exit 0 and say nothing.
func TestScratchRepairAndSeedQuietWithoutWorkingDisk(t *testing.T) {
	scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", "")
	for _, m := range []string{"--repair", "--seed"} {
		if out := runScratch(t, m); out != "" {
			t.Errorf("af-scratch %s without a working disk printed %q", m, out)
		}
	}
}

// The entrypoint is what runs at start, so the repair is only as good as its call
// there.
func TestEntrypointRepairsScratchLinks(t *testing.T) {
	b, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"af-scratch --repair", "af-scratch --seed"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("entrypoint.sh does not run %q", want)
		}
	}
}

func isUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}
