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

func trimmerFor(dir string, now time.Time, free, total uint64, on bool) trimmer {
	return trimmer{
		dir:     func() string { return dir },
		free:    func() (uint64, uint64, bool) { return free, total, true },
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
