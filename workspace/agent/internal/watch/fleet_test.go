package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFleet is the Agent's view of the Workspace's sessions.
type fakeFleet struct {
	mu      sync.Mutex
	visible map[string]bool // caller|name
	sess    map[string]*FleetObservation
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{visible: map[string]bool{}, sess: map[string]*FleetObservation{}}
}

func (f *fakeFleet) Visible(caller, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.visible[caller+"|"+name]
}

func (f *fakeFleet) Observe(name string) (FleetObservation, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.sess[name]
	if !ok {
		return FleetObservation{}, false
	}
	return *o, true
}

func (f *fakeFleet) set(name string, o FleetObservation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sess[name] = &o
}

func (f *fakeFleet) drop(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sess, name)
}

const (
	turn1 = "2026-11-13T10:00:00Z"
	turn2 = "2026-11-13T10:05:00Z"
	turn3 = "2026-11-13T10:09:00Z"
)

func idleAt(turn string) FleetObservation {
	return FleetObservation{Alive: true, Idle: true, LastTurnEndAt: turn}
}

func working(turn string) FleetObservation {
	return FleetObservation{Alive: true, LastTurnEndAt: turn}
}

type fleetRig struct {
	e    *Engine
	st   *memStore
	c    *clock
	sink *fakeSink
	fl   *fakeFleet
}

func newFleetRig() *fleetRig {
	fl := newFakeFleet()
	fl.visible["parent|kid"] = true
	sink := &fakeSink{answer: Ready}
	e, st, c := newTestEngine(&FleetSource{Fleet: fl}, sink)
	return &fleetRig{e: e, st: st, c: c, sink: sink, fl: fl}
}

// tick moves the clock past the source's read interval and runs one engine tick.
func (r *fleetRig) tick() {
	r.c.add(20 * time.Second)
	r.e.Tick(context.Background())
}

func (r *fleetRig) watch(t *testing.T, until string, types ...string) Subscription {
	t.Helper()
	if len(types) == 0 {
		types = []string{FleetIdle}
	}
	sub, err := r.e.Subscribe(context.Background(), Request{Owner: "parent", Source: FleetKind, Ref: "kid", Types: types, Until: until})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestFleetResolveScope(t *testing.T) {
	fl := newFakeFleet()
	fl.visible["parent|kid"] = true
	fl.visible["parent|parent"] = true // a Visible that is wrong about itself must still not matter
	fl.visible["child|parent"] = true
	fl.set("kid", idleAt(turn1))
	fl.set("stranger", idleAt(turn1))
	fl.set("parent", idleAt(turn1))
	src := &FleetSource{Fleet: fl}
	ctx := WithOwner(WithSubscriber(context.Background(), "parent"), "parent")

	res, err := src.Resolve(ctx, "kid", "")
	if err != nil || res.Subject != "fleet:session/kid" {
		t.Fatalf("a visible child: %+v %v", res, err)
	}

	var refused [2]string
	for i, ref := range []string{"stranger", "nobody"} { // exists but not visible / does not exist
		_, err := src.Resolve(ctx, ref, "")
		var er *ErrRefused
		if !errors.As(err, &er) {
			t.Fatalf("%s: want a refusal, got %v", ref, err)
		}
		refused[i] = strings.ReplaceAll(er.Reason, ref, "X")
	}
	if refused[0] != refused[1] {
		t.Fatalf("an invisible session and a missing one must be refused alike: %q vs %q", refused[0], refused[1])
	}

	for _, ref := range []string{"parent", "", "  ", "a b", "kid\nx", "../kid"} {
		if _, err := src.Resolve(ctx, ref, ""); err == nil {
			t.Errorf("ref %q was accepted", ref)
		} else if _, ok := err.(*ErrRefused); !ok {
			t.Errorf("ref %q: %T, want *ErrRefused", ref, err)
		}
	}

	// notify parent: the child asks, the parent receives; neither may be the subject.
	ctx = WithOwner(WithSubscriber(context.Background(), "kid"), "parent")
	if _, err := src.Resolve(ctx, "parent", ""); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("a child watching the parent it reports to: %v", err)
	}
	if _, err := src.Resolve(WithSubscriber(context.Background(), "parent"), "parent", ""); err == nil {
		t.Fatal("a watch on yourself was accepted")
	}
}

// A child that is idle when the watch is made is history: the next turn end is the news.
func TestFleetIdleAtWatchTimeFiresOnTheNextTurnEnd(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", idleAt(turn1))
	r.watch(t, "")
	r.tick()
	r.tick()
	if len(r.sink.told) != 0 {
		t.Fatalf("told about a turn that ended before the watch: %v", r.sink.told)
	}
	r.fl.set("kid", working(turn1))
	r.tick()
	r.fl.set("kid", idleAt(turn2))
	r.tick()
	r.tick()
	if len(r.sink.told) != 1 || r.sink.told[0] != "parent:fleet.session.idle:"+turn2 {
		t.Fatalf("told = %v", r.sink.told)
	}
}

// The parent messages an idle child and watches it. The list may still read idle with the old
// turn end for a while after the message was delivered; that must not end the wait.
func TestFleetIdleChildThatIsMessagedFiresOnTheNextTurnEndNotAtOnce(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", idleAt(turn1))
	r.watch(t, "")
	for i := 0; i < 3; i++ { // the lag: still idle on turn1
		r.tick()
	}
	if len(r.sink.told) != 0 {
		t.Fatalf("fired for the turn before the message: %v", r.sink.told)
	}
	r.fl.set("kid", working(turn1))
	r.tick()
	r.fl.set("kid", idleAt(turn2))
	r.tick()
	r.tick()
	if len(r.sink.told) != 1 || !strings.HasSuffix(r.sink.told[0], turn2) {
		t.Fatalf("told = %v", r.sink.told)
	}
}

func TestFleetWorkingChildFiresWhenItFinishes(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", working(""))
	r.watch(t, "")
	r.tick()
	r.fl.set("kid", idleAt(turn1))
	r.tick()
	r.tick()
	if len(r.sink.told) != 1 || !strings.HasSuffix(r.sink.told[0], turn1) {
		t.Fatalf("told = %v", r.sink.told)
	}
	if got := r.e.List("parent"); len(got) != 0 {
		t.Fatalf("until first must end the watch: %v", got)
	}
}

func TestFleetEachTurnEndIsOneNoticeAndRestartRepeatsNone(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", working(""))
	r.watch(t, UntilDeadline)
	r.fl.set("kid", idleAt(turn1))
	r.tick()
	r.tick()
	r.tick() // idle and unchanged: no repeat
	if len(r.sink.told) != 1 {
		t.Fatalf("told = %v", r.sink.told)
	}
	// An Agent restart: a new engine over the same state.
	e2 := New(r.st, r.sink, &FleetSource{Fleet: r.fl})
	e2.now = r.c.now
	for i := 0; i < 3; i++ {
		r.c.add(20 * time.Second)
		e2.Tick(context.Background())
	}
	if len(r.sink.told) != 1 {
		t.Fatalf("told again after a restart: %v", r.sink.told)
	}
	r.fl.set("kid", working(turn1))
	r.c.add(20 * time.Second)
	e2.Tick(context.Background())
	r.fl.set("kid", idleAt(turn2))
	for i := 0; i < 3; i++ {
		r.c.add(20 * time.Second)
		e2.Tick(context.Background())
	}
	if len(r.sink.told) != 2 || !strings.HasSuffix(r.sink.told[1], turn2) {
		t.Fatalf("the next turn end is the next notice: %v", r.sink.told)
	}
}

func TestFleetStopped(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", FleetObservation{})
	r.watch(t, UntilDeadline, FleetStopped)
	r.tick()
	r.tick()
	if len(r.sink.told) != 1 || r.sink.told[0] != "parent:fleet.session.stopped:1" {
		t.Fatalf("a stopped session is reported at once: %v", r.sink.told)
	}
	r.tick()
	if len(r.sink.told) != 1 {
		t.Fatalf("repeated: %v", r.sink.told)
	}
	r.fl.set("kid", working(turn1)) // resumed
	r.tick()
	r.fl.set("kid", FleetObservation{}) // stopped again
	r.tick()
	r.tick()
	if len(r.sink.told) != 2 || r.sink.told[1] != "parent:fleet.session.stopped:2" {
		t.Fatalf("a second stop is a new notice: %v", r.sink.told)
	}
}

func TestFleetQuotaReopened(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", FleetObservation{Alive: true})
	r.watch(t, "", FleetQuotaReopened)
	r.tick()
	r.tick()
	if len(r.sink.told) != 0 {
		t.Fatalf("a session that never hit its limit: %v", r.sink.told)
	}
	r.fl.set("kid", FleetObservation{Alive: true, Limit: "2026-11-13T10:00:00Z"})
	r.tick()
	if len(r.sink.told) != 0 {
		t.Fatalf("told while still limited: %v", r.sink.told)
	}
	r.fl.set("kid", FleetObservation{Alive: true})
	r.tick()
	r.tick()
	if len(r.sink.told) != 1 || r.sink.told[0] != "parent:fleet.quota.reopened:2026-11-13T10:00:00Z" {
		t.Fatalf("told = %v", r.sink.told)
	}
}

func TestFleetDeletedSessionEndsTheWatch(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", working(""))
	r.watch(t, "")
	r.tick()
	r.fl.drop("kid")
	for i := 0; i < 12 && len(r.sink.told) == 0; i++ {
		r.c.add(5 * time.Minute) // the engine spaces the confirming reads by its error backoff
		r.e.Tick(context.Background())
	}
	if len(r.sink.told) != 1 || !strings.HasPrefix(r.sink.told[0], "parent:"+EventEnded) {
		t.Fatalf("told = %v", r.sink.told)
	}
}

func TestFleetTypesAreRequiredAndChecked(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", working(""))
	for _, types := range [][]string{nil, {"time.fired"}, {"fleet.session.bogus"}} {
		_, err := r.e.Subscribe(context.Background(), Request{Owner: "parent", Source: FleetKind, Ref: "kid", Types: types})
		var er *ErrRefused
		if !errors.As(err, &er) {
			t.Fatalf("types %v: %v", types, err)
		}
	}
	if _, err := r.e.Subscribe(context.Background(), Request{Owner: "parent", Source: FleetKind, Ref: "kid", Types: []string{"fleet.session.*"}}); err != nil {
		t.Fatalf("the prefix pattern: %v", err)
	}
}

// The notice is composed from the session name and state only.
func TestFleetNoticeCarriesNoSessionOutput(t *testing.T) {
	r := newFleetRig()
	r.fl.set("kid", working(""))
	sub := r.watch(t, "")
	r.fl.set("kid", idleAt(turn1))
	evs, _, _, err := (&FleetSource{Fleet: r.fl}).Poll(context.Background(), sub.Subject, nil, nil)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%v %v", evs, err)
	}
	n := Notice(sub, evs[0])
	if len(evs[0].Data) != 0 || !strings.Contains(n, "source=fleet type=fleet.session.idle subject=fleet:session/kid") {
		t.Fatalf("notice = %q", n)
	}
}

func (f *fakeFleet) setVisible(caller, name string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visible[caller+"|"+name] = v
}

// A peer watch made while peer messaging was on stops with the permission, for what is already
// pending and across a restart; the parent's watch on its own child, on the same subject, stays.
func TestFleetWatchEndsWhenTheMakerLosesSight(t *testing.T) {
	r := newFleetRig()
	r.fl.setVisible("peer", "kid", true)
	r.sink.answers = map[string]Readiness{"peer": Wait} // its notice stays pending
	r.fl.set("kid", working(""))
	ctx := context.Background()
	if _, err := r.e.Subscribe(ctx, Request{Owner: "peer", Source: FleetKind, Ref: "kid", Types: []string{FleetIdle}, Until: UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	r.watch(t, UntilDeadline)
	r.fl.set("kid", idleAt(turn1))
	r.tick() // both match; the peer's cannot be delivered yet
	r.fl.setVisible("peer", "kid", false)
	e2 := New(r.st, r.sink, &FleetSource{Fleet: r.fl}) // an Agent restart in between
	e2.now = r.c.now
	r.sink.answers = map[string]Readiness{}
	for i := 0; i < 3; i++ {
		r.c.add(20 * time.Second)
		e2.Tick(ctx)
	}
	var peer, parent []string
	for _, s := range r.sink.told {
		if strings.HasPrefix(s, "peer:") {
			peer = append(peer, s)
		} else {
			parent = append(parent, s)
		}
	}
	if len(peer) != 1 || !strings.HasPrefix(peer[0], "peer:"+EventEnded) {
		t.Fatalf("the peer was told %v, want only the ended notice (nothing about the session)", peer)
	}
	if len(parent) != 1 || !strings.HasSuffix(parent[0], turn1) {
		t.Fatalf("the parent's own watch on its child was disturbed: %v", parent)
	}
	r.fl.set("kid", idleAt(turn2))
	for i := 0; i < 3; i++ {
		r.c.add(20 * time.Second)
		e2.Tick(ctx)
	}
	for _, s := range r.sink.told {
		if strings.HasPrefix(s, "peer:") && !strings.HasPrefix(s, "peer:"+EventEnded) {
			t.Fatalf("a revoked watch heard %s", s)
		}
	}
	if len(r.sink.told) != 3 || !strings.HasSuffix(r.sink.told[2], turn2) {
		t.Fatalf("told = %v", r.sink.told)
	}
}

// A limit that lifts between the watch call and the first read is still told, also when the Agent
// restarts in between.
func TestFleetQuotaLiftedBeforeTheFirstRead(t *testing.T) {
	for _, restart := range []bool{false, true} {
		r := newFleetRig()
		r.fl.set("kid", FleetObservation{Alive: true, Limit: "ep1"})
		r.watch(t, "", FleetQuotaReopened)
		r.fl.set("kid", FleetObservation{Alive: true}) // lifted before the first tick
		e := r.e
		if restart {
			e = New(r.st, r.sink, &FleetSource{Fleet: r.fl})
			e.now = r.c.now
		}
		for i := 0; i < 3; i++ {
			r.c.add(20 * time.Second)
			e.Tick(context.Background())
		}
		if len(r.sink.told) != 1 || r.sink.told[0] != "parent:fleet.quota.reopened:ep1" {
			t.Fatalf("restart=%v: told = %v", restart, r.sink.told)
		}
	}
}
