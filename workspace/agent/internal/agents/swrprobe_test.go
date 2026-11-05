package agents

import (
	"sync/atomic"
	"testing"
	"time"
)

type step struct {
	v  int
	ok bool
}

// gated returns a probe that blocks on release until the test lets it answer.
func gated(calls *atomic.Int32, release chan step) func() (int, bool) {
	return func() (int, bool) {
		calls.Add(1)
		s := <-release
		return s.v, s.ok
	}
}

func TestSWRServesStaleWhileRefreshing(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Nanosecond, ColdBudget: 5 * time.Second, Probe: gated(&calls, release)}

	go func() { release <- step{1, true} }()
	if got := p.Get(); got != 1 {
		t.Fatalf("cold Get = %d, want 1", got)
	}
	// Stale now: Get must answer at once with the old value although the refresh is blocked.
	if got := p.Get(); got != 1 {
		t.Fatalf("stale Get = %d, want 1", got)
	}
	for i := 0; i < 20; i++ { // a poll storm must not start a second probe
		p.Get()
	}
	if n := calls.Load(); n > 2 {
		t.Fatalf("probes started = %d, want at most 2 (cold + one refresh)", n)
	}
	release <- step{2, true}
	deadline := time.Now().Add(5 * time.Second)
	for p.Get() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("refresh never landed")
		}
		time.Sleep(time.Millisecond)
		select { // let the next refresh (if one started) finish
		case release <- step{2, true}:
		default:
		}
	}
}

func TestSWRFailureKeepsGoodAnswer(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Nanosecond, ColdBudget: 5 * time.Second, Probe: gated(&calls, release)}
	go func() { release <- step{7, true} }()
	p.Get()
	release2 := make(chan struct{})
	go func() { release <- step{0, false}; close(release2) }()
	p.Get() // starts the failing refresh
	<-release2
	time.Sleep(10 * time.Millisecond)
	go func() { release <- step{0, false} }()
	if got := p.Get(); got != 7 {
		t.Fatalf("after a failed refresh Get = %d, want the last good 7", got)
	}
}

func TestSWRColdCallersShareOneProbe(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Hour, ColdBudget: 5 * time.Second, Probe: gated(&calls, release)}
	res := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func() { res <- p.Get() }()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	release <- step{5, true}
	for i := 0; i < 3; i++ {
		if got := <-res; got != 5 {
			t.Fatalf("cold caller got %d, want 5", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("probes = %d, want 1", n)
	}
}

func TestSWRColdBudgetBounds(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Hour, ColdBudget: 20 * time.Millisecond, Probe: gated(&calls, release)}
	if got := p.Get(); got != 0 {
		t.Fatalf("overrun cold Get = %d, want zero value", got)
	}
	release <- step{9, true} // the late answer serves the next poll
	deadline := time.Now().Add(5 * time.Second)
	for p.Get() != 9 {
		if time.Now().After(deadline) {
			t.Fatal("late answer never stored")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSWRInvalidateTakesEffectAtOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Hour, ColdBudget: 5 * time.Second, Probe: gated(&calls, release)}
	go func() { release <- step{1, true} }()
	p.Get()
	p.Invalidate()
	go func() { release <- step{2, true} }()
	if got := p.Get(); got != 2 {
		t.Fatalf("Get after Invalidate = %d, want the fresh 2, not the stale 1", got)
	}
}

func TestSWRInvalidateDiscardsInFlightProbe(t *testing.T) {
	var calls atomic.Int32
	release := make(chan step)
	p := &SWRProbe[int]{TTL: time.Hour, ColdBudget: 5 * time.Second, Probe: gated(&calls, release)}
	p.Warm() // probe #1 out, started before the "login"
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	p.Invalidate()
	got := make(chan int, 1)
	go func() { got <- p.Get() }() // probe #2
	for calls.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	release <- step{100, true} // the pre-change probe answers: must be dropped
	release <- step{200, true}
	if v := <-got; v != 200 {
		t.Fatalf("Get = %d, want 200 (the pre-invalidate answer 100 must be discarded)", v)
	}
}

func TestSWRColdFailureStored(t *testing.T) {
	var calls atomic.Int32
	p := &SWRProbe[int]{TTL: time.Hour, ColdBudget: 5 * time.Second, StoreColdFailure: true,
		Probe: func() (int, bool) { calls.Add(1); return 0, false }}
	p.Get()
	p.Get()
	if n := calls.Load(); n != 1 {
		t.Fatalf("probes = %d, want 1 (cold failure is an answer)", n)
	}
}
