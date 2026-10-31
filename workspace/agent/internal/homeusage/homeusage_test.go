package homeusage

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A second press while a walk runs must not start another: two walks of a large home would
// double the disk load for one answer. The config func blocks the first walk until released,
// so "still running" is certain rather than a race with a fast disk.
func TestRunnerRunsOneWalkAtATime(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "f"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	calls := 0
	cfg := func() Config {
		calls++
		<-gate
		return Config{Home: home, Repos: filepath.Join(home, "repos"), AfWork: filepath.Join(home, ".af-work")}
	}
	var r Runner
	if !r.Start(cfg) {
		t.Fatal("first start refused")
	}
	if r.Start(cfg) {
		t.Fatal("a second walk started while one runs")
	}
	if s := r.Snapshot(); !s.Running || s.StartedAt == "" || s.Result != nil {
		t.Fatalf("while running: %+v", s)
	}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for r.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("walk never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := r.Snapshot()
	if s.Result == nil || s.Error != "" || s.Result.Total == 0 || calls != 1 {
		t.Fatalf("after: %+v calls=%d", s, calls)
	}
	// The next press walks again.
	if !r.Start(func() Config { return Config{Home: home} }) {
		t.Fatal("a finished walk blocks the next one")
	}
}

// A walk of a home that is gone reports the error and keeps the last good result.
func TestRunnerKeepsTheLastResultOnError(t *testing.T) {
	home := t.TempDir()
	var r Runner
	run := func(h string) Status {
		r.Start(func() Config { return Config{Home: h} })
		for r.Snapshot().Running {
			time.Sleep(5 * time.Millisecond)
		}
		return r.Snapshot()
	}
	if s := run(home); s.Result == nil || s.Error != "" {
		t.Fatalf("first: %+v", s)
	}
	if s := run(filepath.Join(home, "missing")); s.Result == nil || s.Error == "" {
		t.Fatalf("second: %+v", s)
	}
}

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func measure(t *testing.T, cfg Config) *Usage {
	t.Helper()
	var n atomic.Int64
	u, err := Measure(context.Background(), cfg, &n)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// ~/repos as a link into home: the copies are met at the link's target and still filed as
// working copies, not as other.
func TestMeasureFollowsReposLinkedInsideHome(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "data", "repos", "app", "main.go"), 9000)
	if err := os.Symlink(filepath.Join(home, "data", "repos"), filepath.Join(home, "repos")); err != nil {
		t.Fatal(err)
	}
	u := measure(t, Config{Home: home, Repos: filepath.Join(home, "repos"), AfWork: filepath.Join(home, ".af-work")})
	if len(u.Repos) != 1 || u.Repos[0].Name != "app" || u.Repos[0].Bytes == 0 {
		t.Fatalf("repos = %+v", u.Repos)
	}
	if len(u.Elsewhere) != 0 {
		t.Fatalf("elsewhere = %v", u.Elsewhere)
	}
}

// ~/repos as a link out of home is never walked; the answer says so instead of a quiet zero.
func TestMeasureNamesReposOutsideHome(t *testing.T) {
	home, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(out, "app", "main.go"), 9000)
	if err := os.Symlink(out, filepath.Join(home, "repos")); err != nil {
		t.Fatal(err)
	}
	u := measure(t, Config{Home: home, Repos: filepath.Join(home, "repos"), AfWork: filepath.Join(home, ".af-work")})
	if len(u.Elsewhere) != 1 || u.Elsewhere[0] != "~/repos" || len(u.Repos) != 0 {
		t.Fatalf("elsewhere = %v repos = %+v", u.Elsewhere, u.Repos)
	}
}

// A panic inside the walk ends the run with an error rather than leaving it running for good.
func TestRunnerSurvivesAPanic(t *testing.T) {
	home := t.TempDir()
	var r Runner
	r.Start(func() Config {
		return Config{Home: home, AfWorkOrphans: func() (map[string]bool, bool) { panic("boom") }}
	})
	deadline := time.Now().Add(10 * time.Second)
	for r.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("still running after a panic")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s := r.Snapshot(); s.Error == "" {
		t.Fatalf("no error: %+v", s)
	}
}

// A classified root that is itself a link: into home it is filed by its target, out of home
// it is named, never left as a lone link under other.
func TestMeasureResolvesLinkedRoots(t *testing.T) {
	home, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "disk", "cache", "blob"), 50000)
	write(t, filepath.Join(out, "m2", "x.jar"), 50000)
	for target, link := range map[string]string{
		filepath.Join(home, "disk", "cache"): filepath.Join(home, ".cache"),
		filepath.Join(out, "m2"):             filepath.Join(home, ".m2"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	u := measure(t, Config{Home: home, Roots: map[string]Root{
		filepath.Join(home, ".cache"): {Cat: CatToolCache, Part: ".cache"},
		filepath.Join(home, ".m2"):    {Cat: CatToolCache, Part: ".m2"},
	}})
	var tc int64
	for _, c := range u.Categories {
		if c.Key == CatToolCache {
			tc = c.Bytes
		}
	}
	if tc < 50000 {
		t.Fatalf("tool_cache = %d: the linked ~/.cache was not filed by its target", tc)
	}
	if len(u.Elsewhere) != 1 || u.Elsewhere[0] != "~/.m2" {
		t.Fatalf("elsewhere = %v", u.Elsewhere)
	}
}
