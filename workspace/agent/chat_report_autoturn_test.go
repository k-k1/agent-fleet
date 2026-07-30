package main

import (
	"sync"
	"testing"
	"time"
)

// collectRuns waits for n run callbacks (or fails at timeout) and returns the
// per-conversation counts.
func collectRuns(t *testing.T, done <-chan string, n int) map[string]int {
	t.Helper()
	got := map[string]int{}
	timeout := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case c := <-done:
			got[c]++
		case <-timeout:
			t.Fatalf("timed out waiting for run %d/%d (got %v)", i+1, n, got)
		}
	}
	return got
}

func TestAutoTurnSchedulerBundlesWithinWindow(t *testing.T) {
	done := make(chan string, 8)
	s := newAutoTurnScheduler(
		func() time.Duration { return 50 * time.Millisecond },
		func(conv string) { done <- conv },
	)

	// 同じ会話への連打は1回に畳まれ、別会話は独立に発火する。
	s.schedule("c1")
	s.schedule("c1")
	s.schedule("c1")
	s.schedule("c2")
	got := collectRuns(t, done, 2)
	if got["c1"] != 1 || got["c2"] != 1 {
		t.Fatalf("runs = %v, want c1:1 c2:1", got)
	}
	select {
	case c := <-done:
		t.Fatalf("extra run for %s", c)
	case <-time.After(150 * time.Millisecond):
	}

	// 発火済みの窓は閉じている — 次の schedule は新しい窓を開いて再び発火する。
	s.schedule("c1")
	if got := collectRuns(t, done, 1); got["c1"] != 1 {
		t.Fatalf("second window runs = %v", got)
	}
}

func TestAutoTurnSchedulerZeroDelayRunsImmediately(t *testing.T) {
	done := make(chan string, 2)
	s := newAutoTurnScheduler(
		func() time.Duration { return 0 },
		func(conv string) { done <- conv },
	)
	// 遅延 0 = 従来挙動（即時・非同期）。窓を持たないので連打はそのまま複数回走る。
	s.schedule("c1")
	s.schedule("c1")
	if got := collectRuns(t, done, 2); got["c1"] != 2 {
		t.Fatalf("runs = %v, want c1:2", got)
	}
}

func TestAutoTurnSchedulerConcurrentScheduleIsSingleFire(t *testing.T) {
	done := make(chan string, 16)
	s := newAutoTurnScheduler(
		func() time.Duration { return 50 * time.Millisecond },
		func(conv string) { done <- conv },
	)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.schedule("c1")
		}()
	}
	wg.Wait()
	if got := collectRuns(t, done, 1); got["c1"] != 1 {
		t.Fatalf("runs = %v, want c1:1", got)
	}
	select {
	case c := <-done:
		t.Fatalf("extra run for %s", c)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestChatAutoTurnDelayEnvOverride(t *testing.T) {
	if got := chatAutoTurnDelay(); got != chatAutoTurnDelayDefault {
		t.Fatalf("default delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "5")
	if got := chatAutoTurnDelay(); got != 5*time.Second {
		t.Fatalf("env delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "0")
	if got := chatAutoTurnDelay(); got != 0 {
		t.Fatalf("zero delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "junk")
	if got := chatAutoTurnDelay(); got != chatAutoTurnDelayDefault {
		t.Fatalf("invalid env must fall back: %v", got)
	}
}
