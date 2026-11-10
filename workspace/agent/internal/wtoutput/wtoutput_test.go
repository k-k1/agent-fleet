package wtoutput

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// fixture is a parent clone with one linked worktree whose activity is 60 days old.
type fixture struct {
	t        *testing.T
	base     string // the temp root; nothing outside it may change
	main, wt string
	deps     Deps
}

// ignore is the .gitignore the fixture commits. No trailing slash on node_modules, so a link
// of that name is ignored too (a slash pattern matches directories only).
const ignore = "node_modules\n.next/\ntarget/\n.gradle/\nbuild/\ndist/\n"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, base: base, main: filepath.Join(base, "app"), wt: filepath.Join(base, "app@wip")}
	if err := os.MkdirAll(f.main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, f.main, "init", "-q", "-b", "main")
	write(t, filepath.Join(f.main, ".gitignore"), ignore)
	write(t, filepath.Join(f.main, "src", "a.txt"), "a")
	run(t, f.main, "add", "-A")
	run(t, f.main, "commit", "-q", "-m", "init")
	run(t, f.main, "worktree", "add", "-q", "-b", "wip", f.wt)
	proc := filepath.Join(base, "proc")
	if err := os.MkdirAll(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	f.deps = Deps{ProcRoot: proc}
	f.age()
	return f
}

// age backdates what the dormancy check reads.
func (f *fixture) age() {
	old := time.Now().Add(-60 * 24 * time.Hour)
	gd := strings.TrimSpace(run(f.t, f.wt, "rev-parse", "--absolute-git-dir"))
	for _, p := range []string{filepath.Join(gd, "HEAD"), filepath.Join(gd, "index"), filepath.Join(f.wt, ".git")} {
		if err := os.Chtimes(p, old, old); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) mk(rel string) string {
	p := filepath.Join(f.wt, filepath.FromSlash(rel))
	write(f.t, filepath.Join(p, "x", "big.bin"), strings.Repeat("z", 20000))
	return p
}

func (f *fixture) apply(dry bool) Report {
	return Apply(context.Background(), f.deps, "app@wip", f.wt, 30, dry)
}

func TestDropsIgnoredTierAKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	nm, next, tgt, gr := f.mk("node_modules"), f.mk(".next"), f.mk("target"), f.mk(".gradle")
	build, dist := f.mk("build"), f.mk("dist")
	r := f.apply(false)
	if r.Status != "candidate" || len(r.Removed) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for _, p := range []string{nm, next, tgt, gr} {
		if exists(p) {
			t.Errorf("%s still there", p)
		}
	}
	for _, p := range []string{build, dist, filepath.Join(f.wt, "src", "a.txt"), filepath.Join(f.wt, ".git")} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	if out := run(t, f.main, "worktree", "list", "--porcelain"); !strings.Contains(out, f.wt) {
		t.Errorf("worktree no longer registered:\n%s", out)
	}
}

func TestDryRunRemovesNothing(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	r := f.apply(true)
	if r.Status != "candidate" || len(r.Dirs) != 1 || r.Dirs[0].Rel != "node_modules" || r.Bytes == 0 || len(r.Removed) != 0 {
		t.Fatalf("report = %+v", r)
	}
	if !exists(nm) {
		t.Fatal("dry run deleted the directory")
	}
}

func TestForceAddedTrackedFileKeepsItsDirectory(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	run(t, f.wt, "add", "-f", "node_modules/x/big.bin")
	run(t, f.wt, "commit", "-q", "-m", "force")
	f.age()
	r := f.apply(false)
	if len(r.Removed) != 0 || !exists(nm) {
		t.Fatalf("tracked content lost: %+v", r)
	}
}

func TestUntrackedFileRefusesTheWholeWorktree(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	write(t, filepath.Join(f.wt, "notes.txt"), "mine")
	r := f.apply(false)
	if r.Status != "refused" || r.Reason != ReasonDirty || !exists(nm) {
		t.Fatalf("report = %+v", r)
	}
}

func TestUncommittedChangeRefuses(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	write(t, filepath.Join(f.wt, "src", "a.txt"), "changed")
	if r := f.apply(false); r.Reason != ReasonDirty || !exists(nm) {
		t.Fatalf("report = %+v", r)
	}
}

func TestSymlinksAreUnlinkedNeverFollowed(t *testing.T) {
	f := newFixture(t)
	// A shared tree in the parent clone, one on a scratch disk, a dangling link and one that
	// leaves home altogether: each link goes, each target stays.
	shared := filepath.Join(f.main, "node_modules")
	write(t, filepath.Join(shared, "pkg", "index.js"), "1")
	scratch := filepath.Join(f.base, "scratch", "target")
	write(t, filepath.Join(scratch, "debug", "bin"), "2")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	write(t, filepath.Join(outside, "keep"), "3")
	links := map[string]string{
		"node_modules": shared,
		"target":       scratch,
		".next":        filepath.Join(f.base, "nowhere"), // dangling
		".gradle":      outside,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(f.wt, name)); err != nil {
			t.Fatal(err)
		}
	}
	// .next/ , target/ and .gradle/ patterns do not match a link; widen them for this test.
	write(t, filepath.Join(f.main, ".git", "info", "exclude"), "node_modules\n.next\ntarget\n.gradle\n")
	r := f.apply(false)
	if len(r.Removed) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for name := range links {
		if exists(filepath.Join(f.wt, name)) {
			t.Errorf("link %s still there", name)
		}
	}
	for _, p := range []string{filepath.Join(shared, "pkg", "index.js"), filepath.Join(scratch, "debug", "bin"), filepath.Join(outside, "keep")} {
		if !exists(p) {
			t.Errorf("target file %s was deleted through a link", p)
		}
	}
}

func TestSymlinkInsideATierADirectoryIsNotFollowed(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	keep := filepath.Join(f.base, "precious")
	write(t, filepath.Join(keep, "f"), "x")
	if err := os.Symlink(keep, filepath.Join(nm, "link")); err != nil {
		t.Fatal(err)
	}
	if r := f.apply(false); len(r.Removed) != 1 {
		t.Fatalf("report = %+v", r)
	}
	if !exists(filepath.Join(keep, "f")) {
		t.Fatal("a link inside node_modules was followed")
	}
}

func fakeProc(t *testing.T, deps Deps, pid int, cwd string) {
	t.Helper()
	pd := filepath.Join(deps.ProcRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(pd, "cwd")); err != nil {
		t.Fatal(err)
	}
}

func TestProcessUnderACandidateRefuses(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	fakeProc(t, f.deps, 4242, filepath.Join(nm, "x"))
	for _, dry := range []bool{true, false} {
		r := f.apply(dry)
		if r.Status != "refused" || r.Reason != ReasonInUse || !exists(nm) {
			t.Fatalf("dry=%v report = %+v", dry, r)
		}
	}
}

func TestProcessAppearingAfterTheSurveyIsCaughtBeforeTheDelete(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	budget := sizeBudget
	r := Survey(context.Background(), f.deps, "app@wip", f.wt, 30, &budget)
	if r.Status != "candidate" {
		t.Fatalf("survey = %+v", r)
	}
	fakeProc(t, f.deps, 77, nm)
	root, _ := filepath.EvalSymlinks(f.wt)
	if why := recheck(context.Background(), f.deps, root, r.Dirs[0], nm); why != ReasonInUse {
		t.Fatalf("recheck = %q", why)
	}
}

func TestLiveLockedAndRecentWorktreesAreRefused(t *testing.T) {
	f := newFixture(t)
	nm := f.mk("node_modules")
	f.deps.LiveSessions = func(string) []string { return []string{"s1"} }
	if r := f.apply(false); r.Reason != ReasonLive || !exists(nm) {
		t.Fatalf("live: %+v", r)
	}
	f.deps.LiveSessions = nil
	f.deps.Locked = func(string) bool { return true }
	if r := f.apply(false); r.Reason != ReasonLocked || !exists(nm) {
		t.Fatalf("locked: %+v", r)
	}
	f.deps.Locked = nil
	f.deps.LastSession = func(string) time.Time { return time.Now().Add(-24 * time.Hour) }
	if r := f.apply(false); r.Reason != ReasonDormant || !exists(nm) {
		t.Fatalf("recent session: %+v", r)
	}
	f.deps.LastSession = nil
	now := time.Now()
	if err := os.Chtimes(filepath.Join(strings.TrimSpace(run(t, f.wt, "rev-parse", "--absolute-git-dir")), "HEAD"), now, now); err != nil {
		t.Fatal(err)
	}
	if r := f.apply(false); r.Reason != ReasonDormant || !exists(nm) {
		t.Fatalf("recent HEAD: %+v", r)
	}
}

func TestMainCloneIsNotAWorktree(t *testing.T) {
	f := newFixture(t)
	nm := filepath.Join(f.main, "node_modules")
	write(t, filepath.Join(nm, "f"), "x")
	r := Apply(context.Background(), f.deps, "app", f.main, 30, false)
	if r.Reason != ReasonNotWorktree || !exists(nm) {
		t.Fatalf("report = %+v", r)
	}
}

func TestWorkspacePackagesAreCoveredAndEscapesAreNot(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.base, "outside")
	write(t, filepath.Join(outside, "node_modules", "keep"), "k")
	write(t, filepath.Join(f.base, "pkgs-out", "node_modules", "keep"), "k")
	write(t, filepath.Join(f.wt, "package.json"), `{"workspaces":["packages/*","../outside","/abs/path"]}`)
	write(t, filepath.Join(f.wt, "pnpm-workspace.yaml"), "packages:\n  - 'apps/*'\n  - '../pkgs-out'\n")
	write(t, filepath.Join(f.wt, "packages", "a", "package.json"), "{}")
	write(t, filepath.Join(f.wt, "apps", "web", "package.json"), "{}")
	// A package directory that is a link out of the worktree.
	if err := os.Symlink(outside, filepath.Join(f.wt, "packages", "evil")); err != nil {
		t.Fatal(err)
	}
	run(t, f.wt, "add", "-A")
	run(t, f.wt, "commit", "-q", "-m", "pkgs")
	f.age()
	pa, web := f.mk("packages/a/node_modules"), f.mk("apps/web/.next")
	r := f.apply(false)
	if len(r.Removed) != 2 || exists(pa) || exists(web) {
		t.Fatalf("report = %+v", r)
	}
	for _, p := range []string{filepath.Join(outside, "node_modules", "keep"), filepath.Join(f.base, "pkgs-out", "node_modules", "keep")} {
		if !exists(p) {
			t.Errorf("%s outside the worktree was touched", p)
		}
	}
}

func TestNestedRepositoryIsSkipped(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.wt, "package.json"), `{"workspaces":["vendor/*"]}`)
	run(t, f.wt, "add", "-A")
	run(t, f.wt, "commit", "-q", "-m", "ws")
	f.age()
	sub := filepath.Join(f.wt, "vendor", "lib")
	write(t, filepath.Join(sub, "README"), "r")
	run(t, sub, "init", "-q")
	write(t, filepath.Join(sub, ".gitignore"), "node_modules\n")
	nm := filepath.Join(sub, "node_modules")
	write(t, filepath.Join(nm, "f"), "x")
	write(t, filepath.Join(f.main, ".git", "info", "exclude"), "vendor/lib\n")
	if r := f.apply(false); len(r.Removed) != 0 || !exists(nm) {
		t.Fatalf("a nested repository's node_modules was removed: %+v", r)
	}
}
