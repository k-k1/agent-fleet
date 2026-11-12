package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// symlinkAt creates link -> to, making link's parent first.
func symlinkAt(t *testing.T, to, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, link); err != nil {
		t.Fatal(err)
	}
}

func writeRegistry(t *testing.T, state, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, "links"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRegistry(t *testing.T, state string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "links"))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func assertAbsent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s exists: %s", path, why)
	}
}

// A link whose target is lexically under $AF_WS_SCRATCH/artifacts/ can still lead
// out of it when a component on the way is a symlink. mkdir -p would follow it and
// create directories wherever it points, so every such component must be refused.
func TestScratchRepairRefusesSymlinkedComponent(t *testing.T) {
	cases := map[string]func(scratch, outside string) (to string){
		"symlinked parent": func(scratch, outside string) string {
			symlinkAt(t, outside, filepath.Join(scratch, "artifacts", "escape"))
			return filepath.Join(scratch, "artifacts", "escape", "created")
		},
		"symlinked artifacts": func(scratch, outside string) string {
			symlinkAt(t, outside, filepath.Join(scratch, "artifacts"))
			return filepath.Join(scratch, "artifacts", "created")
		},
		"dot-dot": func(scratch, outside string) string {
			rel, err := filepath.Rel(filepath.Join(scratch, "artifacts", "a"), filepath.Join(outside, "created"))
			if err != nil {
				t.Fatal(err)
			}
			return filepath.Join(scratch, "artifacts", "a") + "/" + rel
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			state := scratchShim(t)
			scratch := t.TempDir()
			t.Setenv("AF_WS_SCRATCH", scratch)
			outside := t.TempDir()
			to := setup(scratch, outside)
			link := filepath.Join(t.TempDir(), "target")
			if err := os.Symlink(to, link); err != nil {
				t.Fatal(err)
			}
			writeRegistry(t, state, link+"\n")

			out := runScratch(t, "--repair")

			assertAbsent(t, filepath.Join(outside, "created"), "--repair created it through "+to)
			if !strings.Contains(out, "not recreating") {
				t.Errorf("--repair did not say it refused %s: %q", to, out)
			}
		})
	}
}

// --auto must not follow a symlinked component either when it makes a new link.
func TestScratchAutoRelocateRefusesSymlinkedComponent(t *testing.T) {
	scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	outside := t.TempDir()
	symlinkAt(t, outside, filepath.Join(scratch, "artifacts"))
	repo := newTestRepo(t, "target/\n")

	scratchAutoRelocate(repo)

	assertAbsent(t, filepath.Join(repo, "target"), "--auto linked through a symlinked artifacts dir")
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("--auto created %v outside the scratch disk", entries)
	}
}

// One entry per line is the registry format, so a path with a newline is refused at
// record time instead of being split into two bogus entries.
func TestScratchDoesNotRecordNewlinePath(t *testing.T) {
	state := scratchShim(t)
	t.Setenv("AF_WS_SCRATCH", t.TempDir())
	repo := filepath.Join(t.TempDir(), "odd\nname")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("af-scratch", "target")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("af-scratch target: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "newline") {
		t.Errorf("no warning about the unrecorded path: %q", out)
	}
	if reg := readRegistry(t, state); len(reg) != 0 {
		t.Errorf("registry = %q, want nothing recorded", reg)
	}
}

// Duplicates, garbage lines and a last line without its newline (a write cut short)
// must not stop the valid entries from being revived, and come out deduplicated.
func TestScratchRepairToleratesMessyRegistry(t *testing.T) {
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a", "target"), filepath.Join(dir, "b", "target")
	symlinkAt(t, filepath.Join(scratch, "artifacts", a), a)
	symlinkAt(t, filepath.Join(scratch, "artifacts", b), b)
	writeRegistry(t, state, a+"\n"+a+"\n\nnot a path\n"+b)

	runScratch(t, "--repair")

	assertResolvesToDir(t, a)
	assertResolvesToDir(t, b)
	if reg := readRegistry(t, state); len(reg) != 2 {
		t.Errorf("registry after --repair = %q, want exactly the two links", reg)
	}
}

// The foreground repair is bounded: past AF_SCRATCH_REPAIR_MAX it warns and keeps the
// remaining entries for a later run instead of dropping them.
func TestScratchRepairStopsAtCap(t *testing.T) {
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	t.Setenv("AF_SCRATCH_REPAIR_MAX", "2")
	dir := t.TempDir()
	var links []string
	for _, n := range []string{"a", "b", "c"} {
		l := filepath.Join(dir, n, "target")
		symlinkAt(t, filepath.Join(scratch, "artifacts", l), l)
		links = append(links, l)
	}
	writeRegistry(t, state, strings.Join(links, "\n")+"\n")

	out := runScratch(t, "--repair")

	revived := 0
	for _, l := range links {
		if _, err := os.Stat(l); err == nil {
			revived++
		}
	}
	if revived != 2 {
		t.Errorf("revived %d links, want the cap of 2", revived)
	}
	if !strings.Contains(out, "AF_SCRATCH_REPAIR_MAX=2") {
		t.Errorf("no warning past the cap: %q", out)
	}
	if reg := readRegistry(t, state); len(reg) != 3 {
		t.Errorf("registry = %q, want all three kept", reg)
	}
	runScratch(t, "--repair")
	for _, l := range links {
		assertResolvesToDir(t, l)
	}
}

// Records made while others record and while --repair rewrites the registry must all
// land, once each: the Agent's --auto, a background --seed and a start-time --repair
// can overlap. The registry is pre-filled with dangling links so the rewrite is still
// running when the records arrive; without the lock its final mv drops them.
func TestScratchRegistryUnderConcurrency(t *testing.T) {
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	pre := t.TempDir()
	var body strings.Builder
	for i := 0; i < 150; i++ {
		l := filepath.Join(pre, strconv.Itoa(i), "target")
		symlinkAt(t, filepath.Join(scratch, "artifacts", l), l)
		body.WriteString(l + "\n")
	}
	writeRegistry(t, state, body.String())
	var repos []string
	for i := 0; i < 12; i++ {
		repos = append(repos, newTestRepo(t, "target/\n"))
	}

	var cmds []*exec.Cmd
	start := func(args ...string) {
		c := exec.Command("af-scratch", args...)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	start("--repair")
	time.Sleep(150 * time.Millisecond)
	for _, r := range repos {
		start("--auto", r)
		start("--auto", r) // the same link twice at once
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Errorf("%v: %v", c.Args, err)
		}
	}

	seen := map[string]int{}
	for _, l := range readRegistry(t, state) {
		seen[l]++
	}
	for _, r := range repos {
		if n := seen[filepath.Join(r, "target")]; n != 1 {
			t.Errorf("%s/target recorded %d times, want 1", r, n)
		}
	}
	if len(seen) != 150+len(repos) {
		t.Errorf("registry holds %d distinct links, want %d", len(seen), 150+len(repos))
	}
}

// A scan that fails part-way must not mark itself done, or the links it missed would
// never be found; the next run retries and marks it then. With the marker in place a
// later --seed does not walk again, and the walk stops at the --auto depth.
func TestScratchSeedMarksOnlyOnSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	state := scratchShim(t)
	scratch := t.TempDir()
	t.Setenv("AF_WS_SCRATCH", scratch)
	repos := t.TempDir()
	t.Setenv("AF_SCRATCH_REPOS", repos)
	ours := filepath.Join(repos, "app", "target")
	symlinkAt(t, filepath.Join(scratch, "artifacts", ours), ours)
	deep := filepath.Join(repos, "app", "a", "b", "c", "target") // depth 5 > 3+1
	symlinkAt(t, filepath.Join(scratch, "artifacts", deep), deep)
	locked := filepath.Join(repos, "locked", "x")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(locked), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(locked), 0o755) })

	out := runScratch(t, "--seed")
	assertAbsent(t, filepath.Join(state, "seeded"), "--seed marked itself done after find failed")
	if !strings.Contains(out, "retries") {
		t.Errorf("no warning about the failed scan: %q", out)
	}
	assertResolvesToDir(t, ours) // what it did find is still handled

	if err := os.Chmod(filepath.Dir(locked), 0o755); err != nil {
		t.Fatal(err)
	}
	runScratch(t, "--seed")
	if _, err := os.Stat(filepath.Join(state, "seeded")); err != nil {
		t.Fatalf("--seed did not mark a successful scan: %v", err)
	}
	if _, err := os.Stat(deep); err == nil {
		t.Errorf("--seed went past the --auto depth to %s", deep)
	}

	later := filepath.Join(repos, "late", "target")
	symlinkAt(t, filepath.Join(scratch, "artifacts", later), later)
	runScratch(t, "--seed")
	if _, err := os.Stat(later); err == nil {
		t.Error("--seed walked again although the marker was there")
	}
}

// Runs the entrypoint's own scratch-link block (between its >>> / <<< markers) under
// `bash -e`, the way the entrypoint runs: when it returns, a registered link and a
// pre-registry link must both resolve — nothing is left for the background.
func TestEntrypointScratchLinkRepair(t *testing.T) {
	b, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "# >>> scratch-link-repair")
	j := strings.Index(src, "# <<< scratch-link-repair")
	if i < 0 || j < i {
		t.Fatal("entrypoint.sh has lost its scratch-link-repair markers")
	}
	block := src[i:j]

	state := scratchShim(t)
	scratch := t.TempDir()
	repos := t.TempDir()
	t.Setenv("AF_SCRATCH_REPOS", repos)
	registered := filepath.Join(repos, "one", "target")
	old := filepath.Join(repos, "two", "build")
	symlinkAt(t, filepath.Join(scratch, "artifacts", registered), registered)
	symlinkAt(t, filepath.Join(scratch, "artifacts", old), old)
	writeRegistry(t, state, registered+"\n")

	cmd := exec.Command("bash", "-ec", block)
	cmd.Env = append(os.Environ(), "AF_WS_SCRATCH="+scratch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint block failed: %v: %s", err, out)
	}
	assertResolvesToDir(t, registered)
	assertResolvesToDir(t, old)
	if _, err := os.Stat(filepath.Join(state, "seeded")); err != nil {
		t.Errorf("the block returned before --seed finished: %v", err)
	}
	if !strings.Contains(string(out), "[entrypoint] scratch: af-scratch: recreated") {
		t.Errorf("block output = %q, want the recreated lines in the log", out)
	}
}

func isUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}
