package homeusage

import (
	"os"
	"path/filepath"
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
