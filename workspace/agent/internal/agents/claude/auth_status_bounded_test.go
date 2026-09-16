package claude

import (
	"sync/atomic"
	"testing"
	"time"
)

// GET /connections used to carry `claude auth status` inline. On a busy workspace that CLI
// takes 21-28s (futex contention over the shared CLAUDE_CONFIG_DIR), the ALB cuts the client
// at 60s, and the Settings card never rendered. These pin the contract that replaced it:
// the probe runs off the request path, an overrun serves the previous answer, and a poll
// never launches a second CLI while one is still out.

// statusProbe swaps in a probe the test drives and shrinks the budgets — waiting the real
// budget out to observe an overrun would make the suite slower than the bug.
//
// started/finished are what make this safe to unwind: an overrunning probe outlives the
// Status call that launched it, and restoring the package var while that goroutine still
// reads it is a data race the -race build fails on. Cleanup releases every blocked probe and
// waits for each one that started before putting the package state back.
type statusProbe struct {
	gate     chan struct{} // closed to release probes parked in park()
	started  atomic.Int32
	finished chan struct{}
}

// install replaces probeStatus with body (wrapped in the bookkeeping) for the test's life.
func install(t *testing.T, budget time.Duration, body func(p *statusProbe) map[string]any) *statusProbe {
	t.Helper()
	p := &statusProbe{gate: make(chan struct{}), finished: make(chan struct{}, 16)}

	origProbe, origBudget, origCold := probeStatus, statusBudget, statusColdBudget
	setLast(nil, false)
	probeStatus = func() map[string]any {
		p.started.Add(1)
		defer func() { p.finished <- struct{}{} }()
		return body(p)
	}
	statusBudget, statusColdBudget = budget, budget

	t.Cleanup(func() {
		p.releaseAndWait()
		probeStatus, statusBudget, statusColdBudget = origProbe, origBudget, origCold
		setLast(nil, false)
	})
	return p
}

func setLast(m map[string]any, running bool) {
	stMu.Lock()
	stLast, stRunning = m, running
	stMu.Unlock()
}

// park blocks the probe until the test releases it, standing in for the 20s CLI.
func (p *statusProbe) park() { <-p.gate }

func (p *statusProbe) releaseAndWait() {
	select {
	case <-p.gate:
	default:
		close(p.gate)
	}
	for range int(p.started.Load()) {
		<-p.finished
	}
}

func TestStatusServesPreviousAnswerWhenProbeOverruns(t *testing.T) {
	var slow atomic.Bool
	install(t, 20*time.Millisecond, func(p *statusProbe) map[string]any {
		if slow.Load() {
			p.park()
			return map[string]any{"connected": true, "email": "late@example.com"}
		}
		return map[string]any{"connected": true, "email": "first@example.com"}
	})

	if got, _ := Status()["email"].(string); got != "first@example.com" {
		t.Fatalf("first probe: email = %q, want first@example.com", got)
	}

	// The probe now overruns. The card must still say what it said last time rather than
	// claiming the person is signed out.
	slow.Store(true)
	start := time.Now()
	m := Status()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Status blocked for %v; the budget is meant to bound it", elapsed)
	}
	if got, _ := m["email"].(string); got != "first@example.com" {
		t.Fatalf("overrun: email = %q, want the previous answer first@example.com", got)
	}
	if c, _ := m["connected"].(bool); !c {
		t.Fatal("overrun: connected = false; a timeout is not evidence of being signed out")
	}
}

func TestStatusDoesNotLaunchASecondProbeWhileOneIsOut(t *testing.T) {
	p := install(t, 20*time.Millisecond, func(p *statusProbe) map[string]any {
		p.park()
		return map[string]any{"connected": true}
	})

	// Three polls at the Console's rate while the first CLI is still running. Launching one
	// per poll is what would feed the contention that makes the CLI slow in the first place.
	for range 3 {
		Status()
	}
	if got := p.started.Load(); got != 1 {
		t.Fatalf("launched %d CLI probes, want exactly 1 while one is in flight", got)
	}
}

func TestStatusLetsARealSignedOutAnswerReplaceAStaleOne(t *testing.T) {
	var out atomic.Bool
	install(t, time.Second, func(*statusProbe) map[string]any {
		if out.Load() {
			return map[string]any{"connected": false}
		}
		return map[string]any{"connected": true, "email": "someone@example.com"}
	})

	if c, _ := Status()["connected"].(bool); !c {
		t.Fatal("seed: connected = false, want true")
	}
	// Signing out is an ANSWER, not an overrun: the fallback must not keep serving the stale
	// "connected" card once the probe has actually reported otherwise.
	out.Store(true)
	if c, _ := Status()["connected"].(bool); c {
		t.Fatal("after sign-out: connected = true; a completed probe must replace the last answer")
	}
}
