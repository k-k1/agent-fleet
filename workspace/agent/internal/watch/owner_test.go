package watch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// unreadableStore is a store whose file is there and does not decode.
type unreadableStore struct{ memStore }

func (unreadableStore) Unreadable() bool { return true }

func subscribe(t *testing.T, e *Engine, owner, ref string, types ...string) Subscription {
	t.Helper()
	sub, err := e.Subscribe(context.Background(), Request{Owner: owner, Source: "fake", Ref: ref, Types: types})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// checksRunningOn is what the PR observation leaves for a PR whose checks still run.
func checksRunningOn(e *Engine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Cursors[bSubject] = json.RawMessage(`{"checks_pending":true}`)
}

func TestWakersCountsWhatCanStartTheOwner(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, _, c := newTestEngine(&fakeSource{}, sink)
	subscribe(t, e, "a", "o/r#1", "pr.merged")
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	checksRunningOn(e)
	// A built-in told to the member never puts input into the session.
	member := ciSpec("a")
	member.Subject, member.Notify = "fake:o/r#2", NotifyMember
	e.SyncBuiltins("n", []BuiltinSpec{member})
	// Another owner's, and a parent's watch made by a child (owned by the parent).
	subscribe(t, e, "b", "o/r#3", "pr.merged")
	if _, err := e.Subscribe(context.Background(), Request{Owner: "p", Requester: "a", Source: "fake", Ref: "o/r#4", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}

	if w, tm, ok := e.Wakers("a"); !ok || w != 2 || tm != 0 {
		t.Fatalf("a: watches=%d timers=%d ok=%v, want 2 watches (its own and the built-in) and no timer", w, tm, ok)
	}
	if w, _, _ := e.Wakers("p"); w != 1 {
		t.Fatalf("the child's notify-parent watch is counted on the parent: %d", w)
	}
	if w, tm, ok := e.Wakers("nobody"); !ok || w != 0 || tm != 0 {
		t.Fatalf("an owner with nothing: %d %d %v", w, tm, ok)
	}

	// Expiry is the engine's own sweep; an expired watch no longer wakes anyone.
	c.add(8 * 24 * time.Hour)
	if w, _, _ := e.Wakers("b"); w != 0 {
		t.Fatalf("an expired watch is counted: %d", w)
	}
}

func TestWakersCountsATimerAndAClaimedNotice(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, c, w := newTimeEngine(sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "time", Ref: "in 1h", Wake: true}); err != nil {
		t.Fatal(err)
	}
	if watches, timers, ok := e.Wakers("a"); !ok || watches != 0 || timers != 1 {
		t.Fatalf("timer: watches=%d timers=%d ok=%v", watches, timers, ok)
	}
	// Due and being delivered: claimed (Done) on disk before the injection, still counted while
	// the injection runs, gone once it succeeded.
	c.add(time.Hour + time.Minute)
	var during int
	sink.onInject = func() { _, during, _ = e.Wakers("a") }
	e.Tick(context.Background())
	if during != 1 {
		t.Fatalf("a timer whose notice is being delivered counted %d, want 1", during)
	}
	if _, timers, _ := e.Wakers("a"); timers != 0 {
		t.Fatalf("a timer that fired for the last time is still counted: %d", timers)
	}
	if w.live() != 0 {
		t.Fatalf("the fired timer's booking is still held: %d", w.live())
	}
}

// A claimed notice counts while it is being delivered even when the subscription's own deadline
// has passed in the meantime: the notice is still going into the session.
func TestWakersCountsAClaimedNoticePastItsDeadline(t *testing.T) {
	e, _, c := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	subscribe(t, e, "a", "o/r#1", "pr.merged")
	e.state.Subs[0].Done = true
	c.add(30 * 24 * time.Hour)
	if w, _, _ := e.Wakers("a"); w != 1 {
		t.Fatalf("a notice being delivered past its deadline counted %d, want 1", w)
	}
	e.state.Subs[0].Done = false
	if w, _, _ := e.Wakers("a"); w != 0 {
		t.Fatalf("an expired watch counted %d", w)
	}
}

func TestWakersUnknownWhenTheStateCannotBeRead(t *testing.T) {
	e := New(&unreadableStore{}, &fakeSink{}, &fakeSource{})
	if _, _, ok := e.Wakers("a"); ok {
		t.Fatal("an unreadable state answered as if it knew")
	}
	if _, _, err := e.EndOwner(context.Background(), "a"); err == nil {
		t.Fatal("ending an owner's subscriptions on a state that was not read succeeded")
	}
}

func TestEndOwnerEndsEverythingTheOwnerHoldsAndItsBookings(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, st, c, w := newTimeEngine(sink)
	ctx := context.Background()
	e.sources["fake"] = &fakeSource{}
	subscribe(t, e, "a", "o/r#1", "pr.merged")
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1h", Wake: true}); err != nil {
		t.Fatal(err)
	}
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	subscribe(t, e, "b", "o/r#9", "pr.merged")
	if w.live() != 1 {
		t.Fatalf("setup: %d bookings", w.live())
	}

	ended, unended, err := e.EndOwner(ctx, "a")
	if err != nil || len(unended) != 0 {
		t.Fatalf("EndOwner: %v %v", err, unended)
	}
	if ended != (Ended{Watches: 1, Timers: 1, Builtins: 1}) {
		t.Fatalf("ended = %+v", ended)
	}
	if watches, timers, _ := e.Wakers("a"); watches+timers != 0 {
		t.Fatalf("still held: %d %d", watches, timers)
	}
	if watches, _, _ := e.Wakers("b"); watches != 1 {
		t.Fatalf("another owner's watch was ended: %d", watches)
	}
	if w.live() != 0 {
		t.Fatalf("the timer's wake booking is still at the Control Plane: %d", w.live())
	}
	if len(st.st.Wakes) != 0 {
		t.Fatalf("the booking is still recorded: %v", st.st.Wakes)
	}
	// On disk too, so a restart does not bring them back.
	e2 := New(st, sink, &fakeSource{})
	if watches, timers, _ := e2.Wakers("a"); watches+timers != 0 {
		t.Fatalf("a restart revived what was ended: %d %d", watches, timers)
	}

	// Repeating it is safe and ends nothing more.
	again, unended, err := e.EndOwner(ctx, "a")
	if err != nil || len(unended) != 0 || again != (Ended{}) {
		t.Fatalf("second EndOwner: %+v %v %v", again, unended, err)
	}
	c.add(2 * time.Hour)
	e.Tick(ctx)
	if len(sink.told) != 0 {
		t.Fatalf("a notice arrived after the retire: %v", sink.told)
	}
}

func TestEndOwnerReportsABookingTheControlPlaneDidNotCancel(t *testing.T) {
	e, _, _, w := newTimeEngine(&fakeSink{answer: Wait})
	ctx := context.Background()
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1h", Wake: true}); err != nil {
		t.Fatal(err)
	}
	w.cancelErr = errors.New("control plane unreachable")
	ended, unended, err := e.EndOwner(ctx, "a")
	if err != nil || ended.Timers != 1 || len(unended) != 1 {
		t.Fatalf("EndOwner: %+v %v %v", ended, unended, err)
	}
	// The next sweep asks again once the Control Plane is back, and the booking goes.
	w.cancelErr = nil
	e.wakeRetryAt = time.Time{}
	e.reconcileWakes(ctx)
	if w.live() != 0 {
		t.Fatalf("the booking was never cancelled: %d", w.live())
	}
}

func TestBarredOwnerGetsNoNewSubscriptionAndNoBuiltin(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	barred := map[string]bool{"a": true}
	e.Barred = func(o string) bool { return barred[o] }

	var ref *ErrRefused
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); !errors.As(err, &ref) {
		t.Fatalf("a barred owner was given a watch: %v", err)
	}
	// A child making one for its barred parent.
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Requester: "c", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); !errors.As(err, &ref) {
		t.Fatalf("a watch for a barred parent was accepted: %v", err)
	}
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	if w, _, _ := e.Wakers("a"); w != 0 {
		t.Fatalf("a built-in was made again for a barred owner: %d", w)
	}
	// What goes to the member is not a wake of the session.
	member := ciSpec("a")
	member.Notify = NotifyMember
	e.SyncBuiltins("n", []BuiltinSpec{member})
	if len(e.state.Subs) != 1 {
		t.Fatalf("the member notification was dropped with the barred owner: %d subs", len(e.state.Subs))
	}
	barred["a"] = false
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	checksRunningOn(e)
	if w, _, _ := e.Wakers("a"); w != 1 {
		t.Fatalf("a session that is no longer barred gets its built-in again: %d", w)
	}
}

// A built-in at its delivery cap has nothing pending once claimed, yet its last notice is still
// going into the owner: it counts, and a retire in that moment ends it.
func TestWakersCountsAClaimedCappedBuiltinWhileItIsDelivered(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(&fakeSource{}, sink)
	sp := ciSpec("a")
	sp.Max = 1
	e.SyncBuiltins("ci", []BuiltinSpec{sp})
	observe(e, ev("pr.checks.failed", "h1"))
	var during int
	var ended Ended
	sink.onInject = func() {
		during, _, _ = e.Wakers("a")
		ended, _, _ = e.EndOwner(context.Background(), "a")
	}
	e.Tick(context.Background())
	c.add(time.Minute)
	if during != 1 || ended.Builtins != 1 {
		t.Fatalf("during delivery: counted %d, ended %+v; want 1 and one built-in", during, ended)
	}
	if w, _, _ := e.Wakers("a"); w != 0 {
		t.Fatalf("after the delivery: %d", w)
	}
}

// listed is the number the session row prints as "N watches", so it has to be what the member's
// watch list shows: built-ins and a notice being delivered count as wakers but are not listed.
func TestWakersListedMatchesTheMemberList(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	checksRunningOn(e)
	check := func(step string, wantListed int) {
		t.Helper()
		w, tm, listed, ok := e.WakersListed("a")
		if !ok || listed != wantListed || listed != len(e.List("a")) {
			t.Fatalf("%s: watches=%d timers=%d listed=%d list=%d ok=%v, want listed %d", step, w, tm, listed, len(e.List("a")), ok, wantListed)
		}
	}
	// Only a built-in: it wakes the owner, the member's list is empty.
	if w, _, _ := e.Wakers("a"); w != 1 {
		t.Fatalf("a built-in is not counted as a waker: %d", w)
	}
	check("built-in only", 0)

	subscribe(t, e, "a", "o/r#1", "pr.merged")
	check("one watch", 1)

	// A claimed notice still wakes the owner but has left the list.
	for i := range e.state.Subs {
		if e.state.Subs[i].Builtin == "" {
			e.state.Subs[i].Done = true
		}
	}
	if w, _, _ := e.Wakers("a"); w != 2 {
		t.Fatalf("a claimed notice is not counted as a waker: %d", w)
	}
	check("notice being delivered", 0)
}

// A built-in with nothing pending and no check running is the feature's standing offer, not
// something an idle session waits for: it is not counted (the run of a memo queue closes), yet a
// retire still ends it. Checks still running, or a notice waiting, hold the owner.
func TestIdleBuiltinIsNotAWakerButRunningChecksAre(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	count := func() int { w, _, _ := e.Wakers("a"); return w }
	if n := count(); n != 0 {
		t.Fatalf("an idle built-in is counted: %d", n)
	}
	checksRunningOn(e)
	if n := count(); n != 1 {
		t.Fatalf("a built-in of a PR whose checks run is not counted: %d", n)
	}
	e.mu.Lock()
	delete(e.state.Cursors, bSubject)
	e.mu.Unlock()
	observe(e, ev("pr.checks.failed", "h1"))
	if n := count(); n != 1 {
		t.Fatalf("a built-in with a notice waiting is not counted: %d", n)
	}
	e.state.Subs[0].Pending = nil
	if n := count(); n != 0 {
		t.Fatalf("settled again: %d", n)
	}
	if ended, _, err := e.EndOwner(context.Background(), "a"); err != nil || ended.Builtins != 1 {
		t.Fatalf("a retire no longer ends the idle built-in: %+v %v", ended, err)
	}
}
