package chatx

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

	// Repeated calls for the same conversation fold into one; a different conversation
	// fires independently.
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

	// A window that has already fired is closed — the next schedule opens a new one and
	// fires again.
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
	// Delay 0 = the old behaviour (immediate, asynchronous). With no window, repeated
	// calls really do run several times.
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

func TestQuietReport(t *testing.T) {
	// Default OFF: nothing is suppressed.
	writeUIPrefs(t, `{}`)
	if quietReport(ReportKindAnswerReady, "") {
		t.Fatal("quiet while the setting is OFF")
	}
	// ON: only a normal completion and its correction go quiet. Failures and aborts still
	// run as before.
	writeUIPrefs(t, `{"assistantQuietCompletion":true}`)
	tests := []struct {
		kind, reason string
		quiet        bool
	}{
		{ReportKindAnswerReady, "", true},
		{reportKindReopened, "", true},
		{ReportKindAnswerReady, ReportReasonTurnAborted, false},
		{ReportKindAnswerReady, ReportReasonTurnFailed, false},
		{reportKindReopened, reportReasonReopenCapped, false},
		{"exit", "oom", false},
	}
	for _, tt := range tests {
		if got := quietReport(tt.kind, tt.reason); got != tt.quiet {
			t.Errorf("quietReport(%s, %q) = %v, want %v", tt.kind, tt.reason, got, tt.quiet)
		}
	}
}

func TestChatAutoTurnDelayEnvOverride(t *testing.T) {
	writeUIPrefs(t, `{}`) // isolate HOME so the real ui-prefs are not read
	if got := ChatAutoTurnDelay(); got != ChatAutoTurnDelayDefault {
		t.Fatalf("default delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "5")
	if got := ChatAutoTurnDelay(); got != 5*time.Second {
		t.Fatalf("env delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "0")
	if got := ChatAutoTurnDelay(); got != 0 {
		t.Fatalf("zero delay = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "junk")
	if got := ChatAutoTurnDelay(); got != ChatAutoTurnDelayDefault {
		t.Fatalf("invalid env must fall back: %v", got)
	}
}
