package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// deadlineStub is a runtime that answers State with whatever the test sets and moves to
// "stopped" when stopped, like an ECS service scaled to 0. With fenceRelease set, its
// operation fence blocks until that channel is closed.
type deadlineStub struct {
	mu           sync.Mutex
	state        string
	stops        atomic.Int32
	endpoint     string
	fenceEntered chan struct{}
	fenceRelease chan struct{}
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
func (r *deadlineStub) Endpoint() string { return r.endpoint }
func (r *deadlineStub) Token() string    { return "" }
func (r *deadlineStub) Name() string     { return "deadline-stub" }
func (r *deadlineStub) BootPhase() string {
	return "blocked: no container instance met all of its requirements"
}
func (r *deadlineStub) AcquireOperationFence(ctx context.Context) (func(), error) {
	if r.fenceRelease == nil {
		return func() {}, nil
	}
	close(r.fenceEntered)
	select {
	case <-r.fenceRelease:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// budgetStub is an adapter whose background launch may still act for budget after Start.
type budgetStub struct {
	deadlineStub
	budget time.Duration
}

func (r *budgetStub) LaunchBudget() time.Duration { return r.budget }

// countStub is an adapter that counts its tasks itself, as ECS does.
type countStub struct {
	deadlineStub
	tasks int
	err   error
}

func (r *countStub) RunningTasks(context.Context) (int, error) { return r.tasks, r.err }

func TestStartDeadlineObserve(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ws := store.Workspace{ID: "w1"}

	d := newStartDeadline(nil, 30*time.Minute)
	if d.observe(ws, nil, "starting", now) {
		t.Fatal("the first sighting is overdue")
	}
	if d.observe(ws, nil, "starting", now.Add(29*time.Minute)) {
		t.Fatal("overdue before the deadline")
	}
	if !d.observe(ws, nil, "starting", now.Add(30*time.Minute)) {
		t.Fatal("not overdue at the deadline")
	}

	// Any other state ends the launch; the next `starting` is a new one.
	d.observe(ws, nil, "running", now.Add(31*time.Minute))
	if d.observe(ws, nil, "starting", now.Add(32*time.Minute)) || d.observe(ws, nil, "starting", now.Add(61*time.Minute)) {
		t.Fatal("the clock survived a state other than starting")
	}

	// A Start re-issued after this process first saw the launch restarts the clock, even when
	// no sweep caught the stopped state in between.
	d = newStartDeadline(nil, 30*time.Minute)
	d.observe(ws, nil, "starting", now)
	restarted := ws
	restarted.LastActiveAt = now.Add(20 * time.Minute).Format(time.RFC3339)
	if d.observe(restarted, nil, "starting", now.Add(40*time.Minute)) {
		t.Fatal("a launch 20 minutes old was called overdue")
	}
	if !d.observe(restarted, nil, "starting", now.Add(50*time.Minute)) {
		t.Fatal("not overdue 30 minutes after the re-issued Start")
	}

	// An old last_active_at never shortens the wait: a CP that just restarted grants the
	// launch a whole window from its own first sighting.
	d = newStartDeadline(nil, 30*time.Minute)
	old := ws
	old.LastActiveAt = now.Add(-24 * time.Hour).Format(time.RFC3339)
	if d.observe(old, nil, "starting", now) {
		t.Fatal("overdue on the first sighting because last_active_at is old")
	}

	// An adapter's own background launch outlasts a shorter configured deadline: a Stop
	// inside it would be undone by that launch while the database says "stopped".
	d = newStartDeadline(nil, 10*time.Minute)
	slow := &budgetStub{budget: 20 * time.Minute}
	d.observe(ws, slow, "starting", now)
	if d.observe(ws, slow, "starting", now.Add(15*time.Minute)) {
		t.Fatal("overdue inside the adapter's launch budget")
	}
	if !d.observe(ws, slow, "starting", now.Add(20*time.Minute)) {
		t.Fatal("not overdue once the launch budget ran out")
	}

	for _, off := range []*startDeadline{nil, newStartDeadline(nil, 0)} {
		off.observe(ws, nil, "starting", now)
		if off.observe(ws, nil, "starting", now.Add(24*time.Hour)) {
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
	u.deadline.wg.Wait()
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
	u.deadline.wg.Wait()
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d after a running sweep, want still 1", n)
	}
}

// The decision is re-taken under the fences: a Start that stamped last_active_at while the
// deadline waited for them is a new launch and must not be stopped.
func TestStartDeadlineRechecksUnderTheFences(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	rt := &deadlineStub{state: "starting", fenceEntered: make(chan struct{}), fenceRelease: make(chan struct{})}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)

	done := make(chan struct{})
	go func() {
		d.stop(ctx, rt, ws)
		close(done)
	}()
	select {
	case <-rt.fenceEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("stop never reached the runtime fence")
	}
	setLastActive(t, st, ws.ID, time.Now()) // a Start recorded while the fence was held
	close(rt.fenceRelease)
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
	rt.fenceRelease = nil
	d.stop(ctx, rt, ws)
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d, want 1", n)
	}
}

// The deadline rides the usage sampler, so it must not wait behind a lifecycle operation:
// a held local lock is skipped at once, and a held fence is given up after
// startDeadlineFenceWait.
func TestStartDeadlineDoesNotWaitBehindAnOperation(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	d := newStartDeadline(mgr, 30*time.Minute)

	rt := &deadlineStub{state: "starting"}
	d.seen[ws.ID] = time.Now().Add(-time.Hour)
	lock := mgr.startLockFor(ws.ID)
	lock.Lock()
	began := time.Now()
	d.stop(ctx, rt, ws)
	lock.Unlock()
	if el := time.Since(began); el > time.Second {
		t.Fatalf("stop waited %s behind the local lock", el)
	}
	if n := rt.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d with the local lock held, want 0", n)
	}

	defer func(w time.Duration) { startDeadlineFenceWait = w }(startDeadlineFenceWait)
	startDeadlineFenceWait = 100 * time.Millisecond
	fenced := &deadlineStub{state: "starting", fenceEntered: make(chan struct{}), fenceRelease: make(chan struct{})}
	defer close(fenced.fenceRelease)
	began = time.Now()
	d.stop(ctx, fenced, ws)
	if el := time.Since(began); el > 2*time.Second {
		t.Fatalf("stop waited %s behind the fence", el)
	}
	if n := fenced.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d with the fence held, want 0", n)
	}
}

// `starting` also covers a rollout whose new task already answers while an old one drains.
// An Agent that answers is never stopped by the deadline.
func TestStartDeadlineLeavesAnAnsweringAgent(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	srv := agentSessionsServer(t, `{"sessions":[{"name":"a","alive":true,"state":"working"}]}`)
	rt := &deadlineStub{state: "starting", endpoint: srv.URL}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)

	d.stop(ctx, rt, ws)
	if n := rt.stops.Load(); n != 0 {
		t.Fatalf("Stop calls = %d with an Agent answering, want 0", n)
	}
	srv.Close()
	d.stop(ctx, rt, ws)
	if n := rt.stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d once the Agent is gone, want 1", n)
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

// Where the adapter counts its own tasks, that count decides, not the Agent probe: a task
// that runs is kept even when its Agent does not answer, and a count that cannot be read
// keeps it too.
func TestStartDeadlineTrustsTheAdaptersTaskCount(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	d := newStartDeadline(mgr, 30*time.Minute)

	for _, tc := range []struct {
		name  string
		tasks int
		err   error
		stops int32
	}{
		{"a task runs, its Agent does not answer", 1, nil, 0},
		{"the count cannot be read", 0, errors.New("throttled"), 0},
		{"nothing runs", 0, nil, 1},
	} {
		rt := &countStub{deadlineStub: deadlineStub{state: "starting"}, tasks: tc.tasks, err: tc.err}
		d.seen[ws.ID] = time.Now().Add(-time.Hour)
		d.stop(ctx, rt, ws)
		if n := rt.stops.Load(); n != tc.stops {
			t.Errorf("%s: Stop calls = %d, want %d", tc.name, n, tc.stops)
		}
		setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	}
}

// The stops run off the sampler's walk, and at most startDeadlineWorkers of them at once:
// however many launches are overdue, the walk neither waits for them nor piles up
// goroutines behind one held fence. The next walk hands the workers to the workspaces that
// have not been tried yet, rather than to the same first two again.
func TestStartDeadlineCapsAndRotatesStops(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	d := newStartDeadline(mgr, 30*time.Minute)

	type cand struct {
		w  store.Workspace
		rt *gateStub
	}
	var cands []cand
	for i := 0; i < startDeadlineWorkers+3; i++ {
		ident, err := st.UpsertIdentity(ctx, fmt.Sprintf("cap-%d@example.com", i), fmt.Sprintf("cap-%d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, ident.ID, ws.TenantID, "member")
		if err != nil {
			t.Fatal(err)
		}
		w := ws
		w.ID, w.MembershipID = store.NewID(), m.ID
		cands = append(cands, cand{w, &gateStub{deadlineStub: deadlineStub{state: "starting"}, release: make(chan struct{})}})
	}
	walk := func() []int {
		for _, c := range cands {
			d.dispatch(ctx, c.rt, c.w) // must return at once
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(d.inflightIDs()) < startDeadlineWorkers && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		for waited := 0; waited < 50; waited++ { // let every dispatched stop reach its fence
			n := int32(0)
			for _, c := range cands {
				if !c.rt.released {
					n += c.rt.entered.Load()
				}
			}
			if int(n) >= startDeadlineWorkers {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		var running []int
		for i, c := range cands {
			if c.rt.entered.Load() > 0 && !c.rt.released {
				running = append(running, i)
			}
		}
		for _, i := range running {
			cands[i].rt.released = true
			close(cands[i].rt.release)
		}
		d.wg.Wait()
		return running
	}

	if got := walk(); len(got) != startDeadlineWorkers {
		t.Fatalf("first walk ran stops %v, want %d at once", got, startDeadlineWorkers)
	}
	second := walk()
	if len(second) != startDeadlineWorkers {
		t.Fatalf("second walk ran stops %v, want %d", second, startDeadlineWorkers)
	}
	for _, i := range second {
		if i < startDeadlineWorkers {
			t.Fatalf("second walk retried workspace %d before the untried ones (ran %v)", i, second)
		}
	}
}

// A stop whose runtime call never returns gives its worker back after
// startDeadlineStopBudget; otherwise two hung AWS calls would switch the deadline off.
func TestStartDeadlineStopHasABudget(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
	defer func(b time.Duration) { startDeadlineStopBudget = b }(startDeadlineStopBudget)
	startDeadlineStopBudget = 200 * time.Millisecond
	rt := &hangStub{deadlineStub: deadlineStub{state: "starting"}}
	d := newStartDeadline(mgr, 30*time.Minute)
	d.seen[ws.ID] = time.Now().Add(-time.Hour)

	d.dispatch(ctx, rt, ws)
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hung Stop held its worker past the budget")
	}
	if ids := d.inflightIDs(); len(ids) != 0 {
		t.Fatalf("workers still held: %v", ids)
	}
}

// hangStub's Stop blocks until its context ends, like an AWS call that never answers.
type hangStub struct{ deadlineStub }

func (r *hangStub) Stop(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (d *startDeadline) inflightIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for id := range d.inflight {
		ids = append(ids, id)
	}
	return ids
}

// gateStub counts the stops that reached its fence and holds them there until release.
type gateStub struct {
	deadlineStub
	entered  atomic.Int32
	release  chan struct{}
	released bool
}

func (r *gateStub) AcquireOperationFence(ctx context.Context) (func(), error) {
	r.entered.Add(1)
	select {
	case <-r.release:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
