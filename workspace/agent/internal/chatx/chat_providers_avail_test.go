package chatx

// Postmortem test for the headlessAvailInFlight guard (see its doc comment in
// chat_providers.go). WarmOneShotKind gave headlessAgentAvailable concurrent callers it never
// had before, and without a guard, a test that polled GET /ai-assist/resolution every 20ms
// while the cache was cold spawned a fresh `claude auth status` PER concurrent caller with no
// cap — measured: ~490 real `claude` processes, ~25GiB RSS, OOM-killed the container.
//
// This file proves the fix WITHOUT EVER STARTING A REAL CLI: headlessAvailCheck is a seam
// specifically so a fake, deliberately slow, instrumented check can stand in. Every loop here
// is bounded by an explicit deadline — never "until some condition that depends on an external
// process finishes".

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeadlessAgentAvailableDedupesConcurrentColdChecks(t *testing.T) {
	const kind = "probe-kind-dedupe"
	t.Cleanup(ClearHeadlessAvailableForTest(kind))

	var calls int32
	release := make(chan struct{})
	entered := make(chan struct{})
	var enteredOnce sync.Once

	prevCheck := headlessAvailCheck
	headlessAvailCheck = func(k string) bool {
		if k != kind {
			return prevCheck(k)
		}
		atomic.AddInt32(&calls, 1)
		enteredOnce.Do(func() { close(entered) })
		<-release // held deliberately open, so every racing caller has time to arrive
		return true
	}
	t.Cleanup(func() { headlessAvailCheck = prevCheck })

	var wg sync.WaitGroup
	stopPoll := make(chan struct{})
	// The exact shape that produced the incident: a tight poll loop racing the still-in-flight
	// check. Bounded by stopPoll, closed on a fixed wall-clock deadline below — never by
	// "until warmed", which is what let the real incident's loop run unbounded.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopPoll:
				return
			default:
				headlessAgentAvailable(kind)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	// The exact shape WarmOneShotKind produces for up to 8 unpinned AI-assist features (and
	// then some) firing at once.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			headlessAgentAvailable(kind)
		}()
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the fake check never ran at all")
	}
	// A fixed window for the poll loop and the burst to misbehave in, then release the leader.
	time.Sleep(150 * time.Millisecond)
	close(stopPoll)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("headlessAvailCheck ran %d times for one cold kind under concurrent callers, want 1 "+
			"(this is the exact shape that spawned ~490 real processes)", got)
	}
	if !headlessAgentAvailable(kind) {
		t.Fatal("the cached answer after the leader returned should be true")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a warm cache hit re-ran the check: %d calls, want 1", got)
	}
}

// A leader whose check PANICS must still clear its in-flight slot and wake its waiters.
// Without the deferred cleanup, the panicking goroutine leaves the kind's channel in the map
// forever: every later caller blocks on it, wedging assistant chat and every one-shot — a
// permanent failure from a transient one. The panic is contained here the same way net/http
// contains one in a handler goroutine.
func TestHeadlessAgentAvailablePanicDoesNotWedgeLaterCallers(t *testing.T) {
	const kind = "probe-kind-panic"
	t.Cleanup(ClearHeadlessAvailableForTest(kind))

	var calls int32
	prevCheck := headlessAvailCheck
	headlessAvailCheck = func(k string) bool {
		if k != kind {
			return prevCheck(k)
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("probe: the vendor CLI check blew up")
		}
		return true
	}
	t.Cleanup(func() { headlessAvailCheck = prevCheck })

	func() {
		defer func() { _ = recover() }()
		headlessAgentAvailable(kind)
	}()

	// The second caller must run its OWN check rather than block: the panicking leader cached
	// no answer, so the slot has to be free AND the minute-long cache must not have been
	// poisoned with a zero value.
	got := make(chan bool, 1)
	go func() { got <- headlessAgentAvailable(kind) }()
	select {
	case v := <-got:
		if !v {
			t.Fatalf("second caller got %v, want true (a panicking check must not be cached)", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller blocked: the panicking leader left its in-flight slot behind")
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("checks run = %d, want 2 (one panicked, one retried)", n)
	}
}

// A waiter that joins WHILE the leader's check is running, and whose leader then panics, must
// not read whatever was sitting in headlessAvail[kind] before the leader started — that value
// can be many minutes stale (103-final-review 軽4). Before this fix, the waiter path read
// headlessAvail[kind] unconditionally after waking, so a leader that panicked handed every
// waiter an EXPIRED cached answer instead of the "false" headlessAgentAvailable's own doc
// promises for a dead leader (chat_providers.go's "A leader that dies must still hand the
// waiters an answer (false)").
func TestHeadlessAgentAvailableWaiterGetsFalseNotStaleCacheAfterLeaderPanics(t *testing.T) {
	const kind = "probe-kind-waiter-expired"
	t.Cleanup(ClearHeadlessAvailableForTest(kind))

	// Seed a long-expired cache entry (10 minutes old, value true) — exactly the shape the
	// review measured. It has to predate the leader's own run, so it cannot be written through
	// headlessAgentAvailable itself (that would make it fresh); write the package vars directly.
	headlessAvailMu.Lock()
	headlessAvailAt[kind] = time.Now().Add(-10 * time.Minute)
	headlessAvail[kind] = true
	headlessAvailMu.Unlock()

	entered := make(chan struct{})
	proceed := make(chan struct{})
	prevCheck := headlessAvailCheck
	headlessAvailCheck = func(k string) bool {
		if k != kind {
			return prevCheck(k)
		}
		close(entered)
		<-proceed
		panic("probe: the vendor CLI check blew up while a waiter was already joined")
	}
	t.Cleanup(func() { headlessAvailCheck = prevCheck })

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		defer func() { _ = recover() }()
		headlessAgentAvailable(kind) // becomes the leader: the seeded entry is stale, not fresh
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never entered its check")
	}

	waiterResult := make(chan bool, 1)
	go func() { waiterResult <- headlessAgentAvailable(kind) }() // must join as a waiter, not a second leader

	// Give the waiter a moment to reach the in-flight join before releasing the panic, so it
	// actually exercises the wake-from-wait path rather than a race that skips it.
	time.Sleep(150 * time.Millisecond)
	close(proceed)

	select {
	case <-leaderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never finished")
	}
	select {
	case v := <-waiterResult:
		if v {
			t.Fatal("waiter got the expired cached true instead of false after the leader panicked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter blocked: the panicking leader left its in-flight slot behind")
	}
}
