package gocachetrim

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeGoCache builds a cache tree: entries (dir/name, age, size) plus the top-level files.
func fakeGoCache(t *testing.T, now time.Time, ents map[string]time.Duration) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".cache", "go-build")
	for rel, age := range ents {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 1000), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// dirBytes is the size of every regular file under dir.
func dirBytes(dir string) (n uint64) {
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, _ error) error {
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			n += uint64(info.Size())
		}
		return nil
	})
	return n
}

// trimmerFor fakes free space as `free` plus whatever has been deleted from dir since: removing
// a file really frees its bytes, as on a disk.
func trimmerFor(dir string, now time.Time, free, total uint64, on bool) trimmer {
	start := dirBytes(dir)
	return trimmer{
		dir:     func() string { return dir },
		free:    func() (uint64, uint64, bool) { return free + start - dirBytes(dir), total, true },
		enabled: func() bool { return on },
		now:     func() time.Time { return now },
	}
}

func TestTrimRemovesOnlyOldEntries(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{
		"README":    10 * day, // top-level files are never touched
		"trim.txt":  10 * day,
		"ab/old1-a": 5 * day,
		"ab/old2-d": 3 * day,
		"cd/old3-a": 2 * day,
		"cd/new-a":  time.Hour, // used within the last day: stays
		"zz/odd-a":  9 * day,   // not an entry directory: stays
	})
	// 10% free of 100000: the target needs 10000 bytes, only 3000 are old enough.
	res, trimmed, done := trimmerFor(dir, now, 10000, 100000, true).pass()
	if !trimmed || res.Files != 3 || res.Bytes != 3000 {
		t.Fatalf("res=%+v trimmed=%v", res, trimmed)
	}
	if done {
		t.Fatal("target out of reach: done must be false so the caller backs off")
	}
	for _, gone := range []string{"ab/old1-a", "ab/old2-d", "cd/old3-a"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s should be removed", gone)
		}
	}
	for _, kept := range []string{"README", "trim.txt", "cd/new-a", "zz/odd-a"} {
		if !exists(filepath.Join(dir, kept)) {
			t.Errorf("%s must stay", kept)
		}
	}
}

func TestTrimGoesOldestFirstAndStopsAtTheTarget(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{
		"ab/a-a": 9 * day, "ab/b-a": 8 * day, "ab/c-a": 7 * day, "ab/d-a": 6 * day,
	})
	// total 50000: threshold 7500, target 10000; free 7000 needs 3000 = three entries.
	res, _, done := trimmerFor(dir, now, 7000, 50000, true).pass()
	if res.Files != 3 || !done {
		t.Fatalf("res=%+v done=%v", res, done)
	}
	for _, gone := range []string{"ab/a-a", "ab/b-a", "ab/c-a"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s is among the three oldest and must go", gone)
		}
	}
	if !exists(filepath.Join(dir, "ab/d-a")) {
		t.Fatal("the newest old entry must stay once the target is met")
	}
}

func TestTrimDoesNothingWhenFreeSpaceIsFine(t *testing.T) {
	now := time.Now()
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * 24 * time.Hour})
	if _, trimmed, _ := trimmerFor(dir, now, 15000, 100000, true).pass(); trimmed || !exists(filepath.Join(dir, "ab/a-a")) {
		t.Fatal("free space at the threshold must trim nothing")
	}
}

func TestTrimDoesNothingWhenDisabled(t *testing.T) {
	now := time.Now()
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * 24 * time.Hour})
	g := trimmerFor(dir, now, 1000, 100000, false)
	if _, trimmed, _ := g.pass(); trimmed || !exists(filepath.Join(dir, "ab/a-a")) {
		t.Fatal("the switch is off: nothing may be removed")
	}
}

func TestTrimSkipsACacheOutsideHome(t *testing.T) {
	now := time.Now()
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * 24 * time.Hour})
	g := trimmerFor(dir, now, 1000, 100000, true)
	orig := sameVolumeFn
	sameVolumeFn = func(a, b string) bool { return false }
	defer func() { sameVolumeFn = orig }()
	if _, trimmed, _ := g.pass(); trimmed || !exists(filepath.Join(dir, "ab/a-a")) {
		t.Fatal("a cache on another volume frees nothing in home and must stay")
	}
}

// Removing a file that frees nothing (hard-linked elsewhere, still open) must not count as
// progress: the real free space decides, so the pass reports "not reached" and keeps going.
func TestTrimJudgesTheTargetByRealFreeSpace(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * day, "ab/b-a": 8 * day, "ab/c-a": 7 * day})
	g := trimmerFor(dir, now, 7000, 50000, true) // logical need 3000: all three files
	g.free = func() (uint64, uint64, bool) { return 7000, 50000, true }
	res, _, done := g.pass()
	if done || res.Files != 3 {
		t.Fatalf("free space never rose: want all three tried and done=false, got res=%+v done=%v", res, done)
	}
}

// A two-hex-digit directory replaced by a link after the survey must not send a delete
// outside the cache.
func TestTrimNeverDeletesThroughASwappedDirectory(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * day, "cd/b-a": 8 * day})
	outside := t.TempDir()
	victim := filepath.Join(outside, "a-a")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Old, so that only the no-follow handling (not the mtime re-check) can protect it.
	_ = os.Chtimes(victim, now.Add(-9*day), now.Add(-9*day))
	g := trimmerFor(dir, now, 1000, 50000, true)
	g.beforeRemove = func(rel string) {
		if rel == filepath.Join("ab", "a-a") {
			_ = os.RemoveAll(filepath.Join(dir, "ab"))
			_ = os.Symlink(outside, filepath.Join(dir, "ab"))
		}
	}
	g.pass()
	if !exists(victim) {
		t.Fatal("a file outside the cache was deleted through a swapped directory")
	}
}

// An entry Go used after the survey (mtime refreshed) stays.
func TestTrimSkipsAnEntryUsedSinceTheSurvey(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * day, "ab/b-a": 8 * day})
	g := trimmerFor(dir, now, 1000, 50000, true)
	g.beforeRemove = func(rel string) {
		if rel == filepath.Join("ab", "a-a") {
			_ = os.Chtimes(filepath.Join(dir, rel), now, now)
		}
	}
	g.pass()
	if !exists(filepath.Join(dir, "ab/a-a")) {
		t.Fatal("an entry used after the survey must stay")
	}
	if exists(filepath.Join(dir, "ab/b-a")) {
		t.Fatal("the other old entry should still go")
	}
}

// The cache directory itself may be a link (to a disk on the same volume): it is followed;
// links inside it (an entry directory, an entry) are not.
func TestTrimFollowsTheRootLinkButNotLinksInside(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	real := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * day})
	outside := t.TempDir()
	outFile := filepath.Join(outside, "x-a")
	_ = os.WriteFile(outFile, []byte("keep"), 0o644)
	old := now.Add(-9 * day)
	_ = os.Chtimes(outFile, old, old)
	if err := os.Symlink(outside, filepath.Join(real, "cd")); err != nil { // linked entry directory
		t.Fatal(err)
	}
	if err := os.Symlink(outFile, filepath.Join(real, "ab", "l-a")); err != nil { // linked entry
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "gocache")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	g := trimmerFor(real, now, 1000, 50000, true)
	g.dir = func() string { return link }
	g.pass()
	if exists(filepath.Join(real, "ab/a-a")) {
		t.Fatal("the entry behind the root link should be trimmed")
	}
	if !exists(outFile) {
		t.Fatal("a link inside the cache was followed")
	}
}

func TestTickSetsTheWaitAndRecordsTheLastTrim(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	dir := fakeGoCache(t, now, map[string]time.Duration{"ab/a-a": 9 * day})
	last.mu.Lock()
	last.last = nil
	last.mu.Unlock()
	if w := trimmerFor(dir, now, 40000, 100000, true).tick(); w != checkEvery || Last() != nil {
		t.Fatalf("free space fine: wait=%v last=%v", w, Last())
	}
	if w := trimmerFor(dir, now, 1000, 100000, true).tick(); w != retryAfter {
		t.Fatalf("target out of reach must back off: wait=%v", w)
	}
	if r := Last(); r == nil || r.Files != 1 || r.Bytes != 1000 {
		t.Fatalf("last=%+v", r)
	}
	if w := trimmerFor(dir, now, 1000, 100000, true).tick(); w != retryAfter {
		t.Fatalf("nothing old left: want back-off, wait=%v", w)
	}
}

func TestRunStopsWhenAskedTo(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { Run(func() string { return t.TempDir() }, stop); close(done) }()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
