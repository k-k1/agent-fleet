package agents

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastIdleTick shrinks the observation interval for the duration of a test.
func fastIdleTick(t *testing.T) {
	t.Helper()
	prev := idleTick
	idleTick = 2 * time.Millisecond
	t.Cleanup(func() { idleTick = prev })
}

func TestIdleGrace(t *testing.T) {
	const key = "AF_TEST_IDLE_GRACE"
	for _, tc := range []struct {
		name string
		env  string
		set  bool
		want time.Duration
	}{
		{name: "unset falls back to the default", want: 90 * time.Second},
		{name: "seconds are honoured", env: "5", set: true, want: 5 * time.Second},
		{name: "zero disables auto-stop", env: "0", set: true, want: 0},
		{name: "garbage falls back", env: "しばらく", set: true, want: 90 * time.Second},
		{name: "negative falls back", env: "-1", set: true, want: 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.env)
			}
			if got := IdleGrace(key, 90*time.Second); got != tc.want {
				t.Fatalf("IdleGrace = %v, want %v", got, tc.want)
			}
		})
	}
}

// grace<=0 means "never auto-stop", so no watch is set up at all and the call returns at once.
// If this loop ran anyway, the daemon would be torn down in a workspace that disabled it.
func TestWatchIdleDisabledReturnsImmediately(t *testing.T) {
	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		WatchIdle("test", func() int { return 0 }, func() bool { stopped.Store(true); return true }, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchIdle did not return for grace<=0")
	}
	if stopped.Load() {
		t.Fatal("auto-stop fired even though it was disabled")
	}
}

func TestWatchIdleStopsAfterGrace(t *testing.T) {
	fastIdleTick(t)
	stopped := make(chan struct{})
	go WatchIdle("test", func() int { return 0 }, func() bool { close(stopped); return true }, 10*time.Millisecond)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop even though demand stayed at zero")
	}
}

// Demand coming back restarts the grace period: having seen zero once is not enough to tear
// anything down.
func TestWatchIdleResetsWhileNeeded(t *testing.T) {
	fastIdleTick(t)
	var needs atomic.Int64
	needs.Store(1)
	var stopped atomic.Bool
	go WatchIdle("test", func() int { return int(needs.Load()) },
		func() bool { stopped.Store(true); return true }, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("stopped while there was still demand")
	}
	needs.Store(0)
	deadline := time.Now().Add(2 * time.Second)
	for !stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("did not stop after demand went away")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A false from stopIfIdle means the re-check under the lock found demand again — keep
// watching and restart the count. Giving up here would mean the daemon is never torn down
// again, no matter how idle it later becomes.
func TestWatchIdleKeepsWatchingWhenStopRefuses(t *testing.T) {
	fastIdleTick(t)
	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	go WatchIdle("test", func() int { return 0 }, func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return false
		}
		close(done)
		return true
	}, 5*time.Millisecond)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		mu.Lock()
		got := calls
		mu.Unlock()
		t.Fatalf("the watch did not continue after a refused stop (%d stopIfIdle calls)", got)
	}
}
