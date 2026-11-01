package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/homeusage"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// allocated sums the allocated blocks of p and everything under it, links not followed — the
// figure the walk attributes to a directory it meets.
func allocated(t *testing.T, p string) int64 {
	t.Helper()
	var n int64
	err := filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		n += info.Sys().(*syscall.Stat_t).Blocks * 512
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func homeCat(u *homeusage.Usage, key string) homeusage.Category {
	for _, c := range u.Categories {
		if c.Key == key {
			return c
		}
	}
	return homeusage.Category{}
}

func homeRepo(u *homeusage.Usage, name string) homeusage.Repo {
	for _, r := range u.Repos {
		if r.Name == name {
			return r
		}
	}
	return homeusage.Repo{}
}

// buildHome lays out a home with one of everything the walk classifies.
func buildHome(t *testing.T) string {
	t.Helper()
	home, _ := isolateLeftovers(t)
	for _, c := range toolCaches {
		if c.env != "" {
			t.Setenv(c.env, "") // a GOCACHE of the machine running this would sit outside home
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	wc := filepath.Join(home, "repos", "app")
	writeSized(t, filepath.Join(wc, "src", "main.go"), 9000)
	// A tracked directory named build is source, not output (this repository has docs/build/).
	writeSized(t, filepath.Join(wc, "docs", "build", "guide.md"), 9000)
	gitRun(t, wc, "init", "-q")
	gitRun(t, wc, "add", ".")
	gitRun(t, wc, "commit", "-q", "-m", "x")
	writeSized(t, filepath.Join(wc, "node_modules", "left-pad", "index.js"), 40000)
	writeSized(t, filepath.Join(wc, "packages", "ui", "dist", "ui.js"), 20000)
	writeSized(t, filepath.Join(wc, "build", "out.bin"), 30000)
	// Nothing below an output directory is judged again: its own build/ is output already.
	writeSized(t, filepath.Join(wc, "node_modules", "x", "build", "y.js"), 5000)

	// Not a repository: its file list cannot be read, so nothing in it counts as output.
	writeSized(t, filepath.Join(home, "repos", "loose", "node_modules", "a.js"), 7000)

	// A worktree whose node_modules is a link to another copy's tree, plus a link out of
	// home and a dangling one: each counts as the link alone.
	wt := filepath.Join(home, "repos", "app@wip")
	writeSized(t, filepath.Join(wt, "README"), 100)
	mustSymlink(t, filepath.Join(wc, "node_modules"), filepath.Join(wt, "node_modules"))
	mustSymlink(t, "/usr", filepath.Join(home, "usr-link"))
	mustSymlink(t, filepath.Join(home, "nowhere"), filepath.Join(home, "dangling"))

	writeSized(t, filepath.Join(home, ".cache", "go-build", "ab", "obj"), 50000)
	writeSized(t, filepath.Join(home, ".cache", "misc", "blob"), 3000)
	writeSized(t, filepath.Join(home, ".gradle", "caches", "8.5", "x.jar"), 60000)
	writeSized(t, filepath.Join(home, ".local", "bin", "tool"), 11000)
	writeSized(t, filepath.Join(sessionx.CleanupArchiveDir(), "20260101-000000-s.tar.gz"), 13000)
	writeSized(t, filepath.Join(sessionx.CacheRoot(), "images", "i.png"), 4000)
	writeSized(t, filepath.Join(home, "notes.txt"), 2000)
	// Loose files beside the working copies and the af-work folders are no copy and no folder.
	writeSized(t, filepath.Join(home, "repos", "stray.txt"), 1000)
	writeSized(t, filepath.Join(home, ".af-work", "loose-file"), 1000)

	// A hard link counts once, wherever it is met first.
	writeSized(t, filepath.Join(home, ".npm", "_cacache", "big"), 70000)
	if err := os.Link(filepath.Join(home, ".npm", "_cacache", "big"), filepath.Join(home, "big-again")); err != nil {
		t.Fatal(err)
	}

	writeSized(t, filepath.Join(home, ".af-work", "sgone01", "probe.txt"), 6000)
	writeSized(t, filepath.Join(home, ".af-work", "app", "probe.txt"), 1000) // a working copy's name
	writeSized(t, filepath.Join(home, ".af-work", "sgone01", paths.SessionWorkDirMarker), 0)
	return home
}

func TestMeasureHomeClassifiesEveryByteOnce(t *testing.T) {
	home := buildHome(t)
	var scanned atomic.Int64
	u, err := homeusage.Measure(context.Background(), homeUsageConfig(), &scanned)
	if err != nil {
		t.Fatal(err)
	}
	if u.Truncated || u.Approximate || u.Unreadable != 0 {
		t.Fatalf("flags: %+v", u)
	}

	var sum int64
	for _, c := range u.Categories {
		sum += c.Bytes
	}
	if sum != u.Total {
		t.Fatalf("categories sum to %d, total %d", sum, u.Total)
	}
	// The same figure du gives for home, less home's own directory entry which the walk
	// starts below.
	if out, err := exec.Command("du", "-sx", "--block-size=1", home).Output(); err == nil {
		du, _ := strconv.ParseInt(strings.Fields(string(out))[0], 10, 64)
		self := allocated(t, home) - allocatedBelow(t, home)
		if u.Total != du-self {
			t.Fatalf("total %d, du %d (home's own entry %d)", u.Total, du, self)
		}
	}

	wc := filepath.Join(home, "repos", "app")
	wantRegen := allocated(t, filepath.Join(wc, "node_modules")) + allocated(t, filepath.Join(wc, "build")) +
		allocated(t, filepath.Join(wc, "packages", "ui", "dist"))
	app := homeRepo(u, "app")
	if app.Regenerable != wantRegen || app.Unjudged {
		t.Fatalf("app = %+v, want regenerable %d", app, wantRegen)
	}
	if app.Bytes != allocated(t, wc) {
		t.Fatalf("app bytes = %d, want %d", app.Bytes, allocated(t, wc))
	}
	if r := homeCat(u, homeusage.CatRegenerable); r.Bytes != wantRegen {
		t.Fatalf("regenerable category %d, want %d (a tracked docs/build or the unjudged copy leaked in)", r.Bytes, wantRegen)
	}
	if loose := homeRepo(u, "loose"); !loose.Unjudged || loose.Regenerable != 0 {
		t.Fatalf("loose = %+v", loose)
	}
	if wt := homeRepo(u, "app@wip"); wt.Bytes >= allocated(t, filepath.Join(wc, "node_modules")) {
		t.Fatalf("the linked node_modules was followed: %+v", wt)
	}

	// Exact figures per category, so a nested root filed under its parent (the Agent's cache
	// inside ~/.cache, its data inside ~/.local) shows here even though the total still adds up.
	below := func(p, nested string) int64 { return allocated(t, p) - allocated(t, nested) }
	if got, want := homeCat(u, homeusage.CatToolCache).Bytes,
		below(filepath.Join(home, ".cache"), sessionx.CacheRoot())+allocated(t, filepath.Join(home, ".gradle"))+allocated(t, filepath.Join(home, ".npm")); got != want {
		t.Fatalf("tool_cache = %d, want %d", got, want)
	}
	if got, want := homeCat(u, homeusage.CatTools).Bytes, below(filepath.Join(home, ".local"), paths.AgentDataDir()); got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}
	if got, want := homeCat(u, homeusage.CatAgent).Bytes,
		below(paths.AgentDataDir(), sessionx.CleanupArchiveDir())+allocated(t, sessionx.CacheRoot()); got != want {
		t.Fatalf("agent = %d, want %d", got, want)
	}
	for _, p := range homeCat(u, homeusage.CatSource).Parts {
		t.Fatalf("source has a part %+v: the copies are listed in Repos", p)
	}
	for _, r := range u.Repos {
		if r.Name == "stray.txt" {
			t.Fatal("a file under ~/repos became a working copy")
		}
	}

	tc := homeCat(u, homeusage.CatToolCache)
	if tc.Reclaimable != allocated(t, filepath.Join(home, ".cache", "go-build"))+allocated(t, filepath.Join(home, ".npm", "_cacache")) {
		t.Fatalf("tool cache reclaimable = %d (parts %+v)", tc.Reclaimable, tc.Parts)
	}
	if tr := homeCat(u, homeusage.CatTrash); tr.Bytes == 0 || tr.Reclaimable != tr.Bytes {
		t.Fatalf("trash = %+v", tr)
	}

	if !u.AfWorkJudged || len(u.AfWork) != 2 {
		t.Fatalf("af_work = %+v judged=%v", u.AfWork, u.AfWorkJudged)
	}
	for _, f := range u.AfWork {
		switch f.Name {
		case "sgone01":
			if !f.Orphan || !f.Marked || f.Modified == "" {
				t.Fatalf("sgone01 = %+v", f)
			}
		case "app":
			if f.Orphan || f.Marked {
				t.Fatalf("app = %+v", f)
			}
		}
	}
	if aw := homeCat(u, homeusage.CatAfWork); aw.Reclaimable != allocated(t, filepath.Join(home, ".af-work", "sgone01")) {
		t.Fatalf("af_work reclaimable = %d", aw.Reclaimable)
	}
	if scanned.Load() == 0 {
		t.Fatal("progress never moved")
	}
}

// allocatedSelf is the blocks of p's own entry; allocatedBelow everything under it.
func allocatedSelf(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

func allocatedBelow(t *testing.T, p string) int64 { return allocated(t, p) - allocatedSelf(t, p) }

// Without the session store no af-work folder is called an orphan.
func TestMeasureHomeAfWorkUnjudgedWithoutSessionStore(t *testing.T) {
	home := buildHome(t)
	if err := os.RemoveAll(filepath.Join(home, "sessions")); err != nil {
		t.Fatal(err)
	}
	var scanned atomic.Int64
	u, err := homeusage.Measure(context.Background(), homeUsageConfig(), &scanned)
	if err != nil {
		t.Fatal(err)
	}
	if u.AfWorkJudged {
		t.Fatal("judged without a session store")
	}
	for _, f := range u.AfWork {
		if f.Orphan {
			t.Fatalf("%s called an orphan", f.Name)
		}
	}
	if aw := homeCat(u, homeusage.CatAfWork); aw.Reclaimable != 0 {
		t.Fatalf("reclaimable = %d", aw.Reclaimable)
	}
}

// A tool cache pointed outside home is never walked, so it is no root: listing it would show
// a zero for a cache that may hold gigabytes.
func TestHomeRootsLeaveOutCachesOutsideHome(t *testing.T) {
	home, _ := isolateLeftovers(t)
	for _, c := range toolCaches {
		if c.env != "" {
			t.Setenv(c.env, "")
		}
	}
	outside := t.TempDir()
	t.Setenv("GOCACHE", outside)
	r := homeRoots(home)
	if _, ok := r[outside]; ok {
		t.Fatal("a GOCACHE outside home is a root")
	}
	if _, ok := r[filepath.Join(home, ".cache", "uv")]; !ok {
		t.Fatal("uv inside home is no root")
	}
}
