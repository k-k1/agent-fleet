package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// deadlineStub is a runtime that answers State with whatever the test sets and moves to
// "stopped" when stopped, like an ECS service scaled to 0.
type deadlineStub struct {
	mu    sync.Mutex
	state string
	stops atomic.Int32
}

func (r *deadlineStub) Start(context.Context) error { return nil }
func (r *deadlineStub) Stop(context.Context) error {
	r.stops.Add(1)
	r.mu.Lock()
	r.state = "stopped"
	r.mu.Unlock()
	return nil
}
func (r *deadlineStub) State(context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}
func (r *deadlineStub) Endpoint() string { return "" }
func (r *deadlineStub) Token() string    { return "" }
func (r *deadlineStub) Name() string     { return "deadline-stub" }
func (r *deadlineStub) BootPhase() string {
	return "blocked: no container instance met all of its requirements"
}

func TestStartDeadlineObserve(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ws := store.Workspace{ID: "w1"}

	d := newStartDeadline(nil, 30*time.Minute)
	if d.observe(ws, "starting", now) {
		t.Fatal("the first sighting is overdue")
	}
	if d.observe(ws, "starting", now.Add(29*time.Minute)) {
		t.Fatal("overdue before the deadline")
	}
	if !d.observe(ws, "starting", now.Add(30*time.Minute)) {
		t.Fatal("not overdue at the deadline")
	}

	// Any other state ends the launch; the next `starting` is a new one.
	d.observe(ws, "running", now.Add(31*time.Minute))
	if d.observe(ws, "starting", now.Add(32*time.Minute)) || d.observe(ws, "starting", now.Add(61*time.Minute)) {
		t.Fatal("the clock survived a state other than starting")
	}

	// A Start re-issued after this process first saw the launch restarts the clock, even when
	// no sweep caught the stopped state in between.
	d = newStartDeadline(nil, 30*time.Minute)
	d.observe(ws, "starting", now)
	restarted := ws
	restarted.LastActiveAt = now.Add(20 * time.Minute).Format(time.RFC3339)
	if d.observe(restarted, "starting", now.Add(40*time.Minute)) {
		t.Fatal("a launch 20 minutes old was called overdue")
	}
	if !d.observe(restarted, "starting", now.Add(50*time.Minute)) {
		t.Fatal("not overdue 30 minutes after the re-issued Start")
	}

	// An old last_active_at never shortens the wait: a CP that just restarted grants the
	// launch a whole window from its own first sighting.
	d = newStartDeadline(nil, 30*time.Minute)
	old := ws
	old.LastActiveAt = now.Add(-24 * time.Hour).Format(time.RFC3339)
	if d.observe(old, "starting", now) {
		t.Fatal("overdue on the first sighting because last_active_at is old")
	}

	for _, off := range []*startDeadline{nil, newStartDeadline(nil, 0)} {
		off.observe(ws, "starting", now)
		if off.observe(ws, "starting", now.Add(24*time.Hour)) {
			t.Fatal("a disabled deadline fired")
		}
	}
}

// The wiring through the usage sampler, end to end against the store: an overdue launch is
// stopped and recorded as stopped; a running workspace, and a launch still inside its
// window, are left alone.
func TestSampleStopsAnOverdueStart(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	rt := &deadlineStub{state: "starting"}
	mgr.rtFactory = stubFactory{rt: rt}
	u := newUsageSampler(mgr, 5*time.Minute)
	u.deadline = newStartDeadline(mgr, 30*time.Minute)

	u.sample(ctx)
	if n := rt.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d on the first sighting, want 0", n)
	}

	u.deadline.seen[ws.ID] = time.Now().Add(-31 * time.Minute)
	u.sample(ctx)
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d for a launch 31 minutes old, want 1", n)
	}
	got, _, err := st.GetWorkspaceByMembership(ctx, ws.MembershipID)
	if err != nil || got.State != "stopped" {
		t.Fatalf("recorded state = %q (err %v), want stopped", got.State, err)
	}
	if _, ok := u.deadline.seen[ws.ID]; ok {
		t.Error("the clock of a stopped launch was kept")
	}

	rt.state = "running"
	u.deadline.seen[ws.ID] = time.Now().Add(-31 * time.Minute)
	u.sample(ctx)
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d after a running sweep, want still 1", n)
	}
}

// The decision is re-taken under the fences: a Start that stamped last_active_at while the
// deadline waited for them is a new launch and must not be stopped.
func TestStartDeadlineRechecksUnderTheFences(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	rt := &deadlineStub{state: "starting"}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)

	lock := mgr.startLockFor(ws.ID)
	lock.Lock() // a Start is in flight
	done := make(chan struct{})
	go func() {
		d.stop(ctx, rt, ws)
		close(done)
	}()
	setLastActive(t, st, ws.ID, time.Now()) // what that Start records when it returns
	lock.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not finish")
	}
	if n := rt.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d for a launch re-issued while waiting, want 0", n)
	}

	// Without the new Start the same call does stop, so the test cannot pass by never stopping.
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	d.stop(ctx, rt, ws)
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d, want 1", n)
	}
}

// An approved shared operation owns the lifecycle lease; the deadline must skip the sweep
// rather than stop underneath it.
func TestStartDeadlineRespectsTheLifecycleLease(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	rt := &deadlineStub{state: "starting"}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)

	op := store.NewID()
	now := time.Now().UTC()
	acquired, err := st.AcquireSessionShareOwnerLease(ctx, ws.MembershipID, op, leaseTS(now), leaseTS(now.Add(time.Minute)))
	if err != nil || !acquired {
		t.Fatalf("acquire share lease: acquired=%v err=%v", acquired, err)
	}
	d.stop(ctx, rt, ws)
	if n := rt.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d while the lifecycle lease was held elsewhere, want 0", n)
	}
}
