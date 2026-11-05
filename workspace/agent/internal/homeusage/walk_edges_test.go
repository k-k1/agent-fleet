package homeusage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// blocksOf is the allocated bytes of p's own entry, a link not followed.
func blocksOf(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

// treeBelow sums the allocated bytes of everything under root, root's own entry left out and
// no link followed: what the walk should report as the total of a home it can read in full.
func treeBelow(t *testing.T, root string) int64 {
	t.Helper()
	var n int64
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p != root {
			n += fi.Sys().(*syscall.Stat_t).Blocks * 512
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func catSum(u *Usage) int64 {
	var s int64
	for _, c := range u.Categories {
		s += c.Bytes
	}
	return s
}

func cfgFor(home string) Config {
	return Config{Home: home, Repos: filepath.Join(home, "repos"), AfWork: filepath.Join(home, ".af-work")}
}

// A directory the walk cannot list is counted in Unreadable, its own entry is still a byte of
// home, and the walk goes on: one locked folder must not blank the whole breakdown.
func TestMeasureSurvivesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	home := t.TempDir()
	write(t, filepath.Join(home, "a", "one"), 8000)
	write(t, filepath.Join(home, "locked", "hidden"), 5000)
	write(t, filepath.Join(home, "z", "two"), 4000) // sorts after "locked": proves the walk went on
	locked := filepath.Join(home, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	u := measure(t, cfgFor(home))
	if u.Unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1", u.Unreadable)
	}
	want := blocksOf(t, filepath.Join(home, "a")) + blocksOf(t, filepath.Join(home, "a", "one")) +
		blocksOf(t, locked) +
		blocksOf(t, filepath.Join(home, "z")) + blocksOf(t, filepath.Join(home, "z", "two"))
	if u.Total != want {
		t.Fatalf("total = %d, want %d (what the locked folder holds is in no figure)", u.Total, want)
	}
	if catSum(u) != u.Total {
		t.Fatalf("categories sum to %d, total %d", catSum(u), u.Total)
	}
}

// No link is followed: a link into a sibling directory, to another copy's node_modules, out
// of home and to nothing each count as the link alone, so the tree they point at is counted
// once, where it lives.
func TestMeasureFollowsNoLink(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "data", "big"), 50000)
	write(t, filepath.Join(home, "repos", "app", "node_modules", "pkg", "index.js"), 40000)
	write(t, filepath.Join(home, "repos", "app@wip", "README"), 100)
	write(t, filepath.Join(outside, "huge"), 90000)
	links := map[string]string{
		filepath.Join(home, "sibling"):                          filepath.Join(home, "data"),
		filepath.Join(home, "repos", "app@wip", "node_modules"): filepath.Join(home, "repos", "app", "node_modules"),
		filepath.Join(home, "out"):                              outside,
		filepath.Join(home, "dangling"):                         filepath.Join(home, "nowhere"),
	}
	for l, target := range links {
		if err := os.Symlink(target, l); err != nil {
			t.Fatal(err)
		}
	}

	u := measure(t, cfgFor(home))
	if want := treeBelow(t, home); u.Total != want {
		t.Fatalf("total = %d, want %d: a link was followed or a target counted twice", u.Total, want)
	}
	if catSum(u) != u.Total {
		t.Fatalf("categories sum to %d, total %d", catSum(u), u.Total)
	}
	for _, r := range u.Repos {
		if r.Name == "app@wip" && r.Bytes >= 40000 {
			t.Fatalf("app@wip = %d bytes: its node_modules link was followed", r.Bytes)
		}
	}
}

// Two names for one file are one file's bytes, wherever the second is met.
func TestMeasureCountsAHardLinkOnce(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "a", "big"), 70000)
	if err := os.MkdirAll(filepath.Join(home, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(home, "a", "big"), filepath.Join(home, "b", "big-again")); err != nil {
		t.Fatal(err)
	}

	u := measure(t, cfgFor(home))
	want := treeBelow(t, home) - blocksOf(t, filepath.Join(home, "a", "big"))
	if u.Total != want {
		t.Fatalf("total = %d, want %d (the file counted once)", u.Total, want)
	}
	if u.Approximate {
		t.Fatal("approximate with one hard link")
	}
}
