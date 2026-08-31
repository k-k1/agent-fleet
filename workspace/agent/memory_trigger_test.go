package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 契機判定は純関数なので、実時間を待たずに全分岐を固定できる（docs/log/39 の
// 「idle 遷移 + debounce」をポーリングで表現したもの）。
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
		{"静穏かつ非稼働なら積む", in(nil), true},
		{"対象ファイルが無ければ積まない", in(func(v *memoryTriggerInput) { v.NewestChange = time.Time{} }), false},
		{"前回 snapshot 以降に変更が無ければ積まない",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = base.Add(time.Second) }), false},
		{"変更時刻と前回 snapshot が同時刻なら積まない",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = base }), false},
		{"debounce 未満なら待つ",
			in(func(v *memoryTriggerInput) { v.Now = base.Add(4 * time.Minute) }), false},
		{"debounce ちょうどで積む",
			in(func(v *memoryTriggerInput) { v.Now = base.Add(5 * time.Minute) }), true},
		{"初回（snapshot がまだ無い）でも積む",
			in(func(v *memoryTriggerInput) { v.LastSnapshot = time.Time{} }), true},
		{"稼働中セッションがあれば待つ",
			in(func(v *memoryTriggerInput) { v.Busy = true }), false},
		{"稼働中でも MaxDefer を超えたら押し切る",
			in(func(v *memoryTriggerInput) { v.Busy = true; v.Now = base.Add(31 * time.Minute) }), true},
		{"MaxDefer=0 なら稼働中は永遠に待つ",
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

// AF_MEMORY_SNAPSHOT は既定 ON（docs/log/39 決着 #1）。off 指定だけがループを止める。
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
	t.Setenv("AF_MEMORY_SNAPSHOT_INTERVAL", "1ms") // 下限未満は既定へ
	if d := memorySnapshotInterval(); d != memoryDefaultInterval {
		t.Fatalf("below-minimum interval should fall back, got %v", d)
	}
}

// 1 周期分の実行を実データで通す: debounce 前は積まず、静穏後に積み、その直後は
// 「前回 snapshot 以降に変更なし」で再び積まない。
func TestMemorySnapshotTick(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	mem := filepath.Join(cfg, "projects", slug, "memory", "a.md")
	changed := time.Now().Add(-time.Minute)
	if err := os.Chtimes(mem, changed, changed); err != nil {
		t.Fatal(err)
	}
	// 他のメモリも同じ「1 分前」に揃えて、最新 mtime を決定的にする。
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
