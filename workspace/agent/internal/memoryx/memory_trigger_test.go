package memoryx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The trigger decision is a pure function, so every branch can be pinned down without
// waiting on real time (docs/log/39's "idle transition + debounce", expressed as polling).
func TestMemoryShouldSnapshot(t *testing.T) {
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	in := func(mod func(*memoryTriggerInput)) memoryTriggerInput {
		v := memoryTriggerInput{
			Now:          base.Add(10 * time.Minute),
			NewestChange: base,
			LastSnapshot: base.Add(-time.Hour),
			Debounce:     5 * time.Minute,
			MaxDefer:     30 * time.Minute,
		}
		if mod != nil {
			mod(&v)
		}
		return v
	}
	cases := []struct {
		name string
		in   memoryTriggerInput
		want bool
	}{
		{"quiet and not busy: snapshot", in(nil), true},
		{"no target file: no snapshot", in(func(v *memoryTriggerInput) { v.NewestChange = time.Time{} }), false},
		{"no change since the last snapshot: no snapshot",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = base.Add(time.Second) }), false},
		{"change time equal to the last snapshot: no snapshot",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = base }), false},
		{"below debounce: wait",
			in(func(v *memoryTriggerInput) { v.Now = base.Add(4 * time.Minute) }), false},
		{"exactly at debounce: snapshot",
			in(func(v *memoryTriggerInput) { v.Now = base.Add(5 * time.Minute) }), true},
		{"first run, no snapshot yet: snapshot",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = time.Time{} }), true},
		{"a busy session: wait",
			in(func(v *memoryTriggerInput) { v.Busy = true }), false},
		{"busy but past MaxDefer: go ahead anyway",
			in(func(v *memoryTriggerInput) { v.Busy = true; v.Now = base.Add(31 * time.Minute) }), true},
		{"MaxDefer=0: wait forever while busy",
			in(func(v *memoryTriggerInput) { v.Busy = true; v.MaxDefer = 0; v.Now = base.Add(99 * time.Hour) }), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := memoryShouldSnapshot(c.in); got != c.want {
				t.Fatalf("memoryShouldSnapshot(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// AF_MEMORY_SNAPSHOT defaults to ON (docs/log/39 decision #1). Only an explicit off
// stops the loop.
func TestMemoryAutoEnabledDefaults(t *testing.T) {
	if !memoryAutoEnabled() {
		t.Fatal("auto snapshot should default to ON")
	}
	for _, v := range []string{"off", "OFF", "0", "false"} {
		t.Setenv("AF_MEMORY_SNAPSHOT", v)
		if memoryAutoEnabled() {
			t.Errorf("AF_MEMORY_SNAPSHOT=%q should disable auto snapshot", v)
		}
	}
	t.Setenv("AF_MEMORY_SNAPSHOT", "on")
	if !memoryAutoEnabled() {
		t.Error("AF_MEMORY_SNAPSHOT=on should keep auto snapshot enabled")
	}
}

func TestMemoryEnvDurationOverrides(t *testing.T) {
	if d := memorySnapshotDebounce(); d != memoryDefaultDebounce {
		t.Fatalf("default debounce = %v", d)
	}
	t.Setenv("AF_MEMORY_SNAPSHOT_DEBOUNCE", "90s")
	if d := memorySnapshotDebounce(); d != 90*time.Second {
		t.Fatalf("override debounce = %v", d)
	}
	t.Setenv("AF_MEMORY_SNAPSHOT_DEBOUNCE", "not-a-duration")
	if d := memorySnapshotDebounce(); d != memoryDefaultDebounce {
		t.Fatalf("invalid override should fall back, got %v", d)
	}
	t.Setenv("AF_MEMORY_SNAPSHOT_INTERVAL", "1ms") // below the minimum falls back to the default
	if d := memorySnapshotInterval(); d != memoryDefaultInterval {
		t.Fatalf("below-minimum interval should fall back, got %v", d)
	}
}

// Run one whole cycle against real data: nothing before debounce, a snapshot once quiet,
// and none immediately after that, because there is no change since the last snapshot.
func TestMemorySnapshotTick(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	mem := filepath.Join(cfg, "projects", slug, "memory", "a.md")
	changed := time.Now().Add(-time.Minute)
	if err := os.Chtimes(mem, changed, changed); err != nil {
		t.Fatal(err)
	}
	// Line the other memories up on the same "one minute ago" so the newest mtime is
	// deterministic.
	for _, r := range memoryRoots() {
		for _, f := range memoryCollect(r) {
			if err := os.Chtimes(f.Abs, changed, changed); err != nil {
				t.Fatal(err)
			}
		}
	}
	debounce, maxDefer := 5*time.Minute, 30*time.Minute

	if memorySnapshotTick(changed.Add(time.Minute), debounce, maxDefer) {
		t.Fatal("tick committed before the debounce elapsed")
	}
	if memoryCommitCount(t) != 0 {
		t.Fatal("a commit was created before the debounce elapsed")
	}
	if !memorySnapshotTick(changed.Add(6*time.Minute), debounce, maxDefer) {
		t.Fatal("tick did not commit after the debounce elapsed")
	}
	if n := memoryCommitCount(t); n != 1 {
		t.Fatalf("after the first tick: %d commits", n)
	}
	if memorySnapshotTick(changed.Add(20*time.Minute), debounce, maxDefer) {
		t.Fatal("tick committed again with no new changes")
	}
	if n := memoryCommitCount(t); n != 1 {
		t.Fatalf("idle tick changed the history: %d commits", n)
	}
}
