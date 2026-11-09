package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWaker is the Control Plane's wake target.
type fakeWaker struct {
	mu        sync.Mutex
	booked    map[string]time.Time
	cancelled []string
	bookErr   error
	cancelErr error
	n         int
	tries     int // Cancel calls, failed ones included
}

func (f *fakeWaker) Book(_ context.Context, at time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bookErr != nil {
		return "", f.bookErr
	}
	f.n++
	id := "sch_" + string(rune('a'+f.n))
	if f.booked == nil {
		f.booked = map[string]time.Time{}
	}
	f.booked[id] = at
	return id, nil
}

func (f *fakeWaker) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if f.cancelErr != nil {
		return f.cancelErr
	}
	delete(f.booked, id)
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeWaker) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.booked)
}

func newTimeEngine(sink Sink) (*Engine, *memStore, *clock, *fakeWaker) {
	st := &memStore{}
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	e := New(st, sink, &Time{Now: c.now})
	e.now = c.now
	w := &fakeWaker{}
	e.SetWaker(w)
	return e, st, c, w
}

func TestTimeResolve(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	src := &Time{Now: c.now}
	ctx := context.Background()
	ok := map[string]string{
		"in 20m":                      "time:20261109T122000Z",
		"20m":                         "time:20261109T122000Z",
		"2h30m":                       "time:20261109T143000Z",
		"1d":                          "time:20261110T120000Z",
		"1d12h":                       "time:20261111T000000Z",
		"2026-11-09T23:00:00+09:00":   "time:20261109T140000Z", // stored in UTC
		"2026-11-09T21:00:00Z":        "time:20261109T210000Z",
		"  IN 5m  ":                   "time:20261109T120500Z",
		"2026-12-09T11:59:00+00:00":   "time:20261209T115900Z",
		"2026-11-09T12:00:30.5+00:00": "time:20261109T120031Z", // rounded up, never early
	}
	for ref, want := range ok {
		got, err := src.Resolve(ctx, ref, "")
		if err != nil || string(got.Subject) != want || !TokenOK(string(got.Subject)) {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got.Subject, err, want)
		}
		if !got.At.Equal(mustSubjectTime(t, want)) {
			t.Errorf("Resolve(%q).At = %v", ref, got.At)
		}
	}
	for _, ref := range []string{
		"", "tomorrow", "15:00", "at 15:00", "2026-11-09T15:00:00", "2026-11-09 15:00", "-5m", "0s", "0d",
		"in", "2026-11-09T11:59:59Z", "2026-11-09T12:00:00Z", // past, and exactly now
		"31d", "2027-01-01T00:00:00Z", "99999h", "401d", "1x", "d",
	} {
		_, err := src.Resolve(ctx, ref, "")
		var rf *ErrRefused
		if !errors.As(err, &rf) {
			t.Errorf("Resolve(%q) = %v, want a refusal", ref, err)
		}
	}
}

func mustSubjectTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := timeSubjectInstant(SubjectID(s))
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestTimePoll(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	src := &Time{Now: c.now}
	ctx := context.Background()
	subj := SubjectID("time:20261109T130000Z")

	evs, _, hint, err := src.Poll(ctx, subj, nil, nil)
	if err != nil || len(evs) != 0 || hint.After != timeMaxPoll {
		t.Fatalf("an hour early: %v %v %+v", evs, err, hint)
	}
	c.t = time.Date(2026, 11, 9, 12, 59, 40, 0, time.UTC)
	if _, _, hint, _ = src.Poll(ctx, subj, nil, nil); hint.After != 20*time.Second {
		t.Fatalf("near the instant the read comes back exactly when it is due: %+v", hint)
	}
	c.t = time.Date(2026, 11, 9, 13, 0, 1, 0, time.UTC)
	evs, _, _, _ = src.Poll(ctx, subj, nil, nil)
	if len(evs) != 1 || evs[0].Type != TimeEvent || evs[0].Version != "20261109T130000Z" || strings.Contains(evs[0].Summary, "late") {
		t.Fatalf("on time: %+v", evs)
	}
	c.t = time.Date(2026, 11, 9, 13, 7, 0, 0, time.UTC)
	evs, _, _, _ = src.Poll(ctx, subj, nil, nil)
	if len(evs) != 1 || !strings.Contains(evs[0].Summary, "7 min late") || evs[0].Version != "20261109T130000Z" {
		t.Fatalf("late: %+v (the version must not depend on when it was read)", evs)
	}
	if _, _, _, err := src.Poll(ctx, "time:garbage", nil, nil); err == nil {
		t.Fatal("a malformed subject was read")
	}
}

func TestTimeWatchFiresOnceAcrossARestart(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, st, c, _ := newTimeEngine(sink)
	ctx := context.Background()
	sub, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m"})
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Due.Equal(c.t.Add(20*time.Minute)) || sub.Wake {
		t.Fatalf("sub = %+v", sub)
	}
	e.Tick(ctx)
	if len(sink.told) != 0 {
		t.Fatalf("fired early: %v", sink.told)
	}
	if l := e.List("a"); len(l) != 1 || !l[0].Due.Equal(sub.Due) {
		t.Fatalf("list_watches = %+v", l)
	}

	c.add(21 * time.Minute)
	// The delivery fails after the claim, as a crash would: the claim is released on a clean
	// failure, so use a sink that accepts, then restart before anything else can run.
	e.Tick(ctx)
	if len(sink.told) != 1 || !strings.HasPrefix(sink.told[0], "a:time.fired:") {
		t.Fatalf("told = %v", sink.told)
	}
	e2 := New(st, sink, &Time{Now: c.now})
	e2.now = c.now
	c.add(time.Minute)
	e2.Tick(ctx)
	e2.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("delivered again after a restart: %v", sink.told)
	}
	if !strings.Contains(sink.notices[0], "kind=watch source=time type=time.fired subject=time:") {
		t.Fatalf("notice = %q", sink.notices[0])
	}
}

// The claim is on disk before the delivery, so a crash between the two loses the notice instead
// of repeating it, for a time watch like any other.
func TestTimeWatchClaimedBeforeDelivery(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, st, c, _ := newTimeEngine(sink)
	ctx := context.Background()
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1m"}); err != nil {
		t.Fatal(err)
	}
	c.add(2 * time.Minute)
	var onDisk State
	sink.onInject = func() { onDisk, _ = st.Load() }
	e.Tick(ctx)
	if len(onDisk.Subs) != 1 || !onDisk.Subs[0].Done {
		t.Fatalf("the claim was not on disk when the notice went out: %+v", onDisk.Subs)
	}
	e2 := New(st, sink, &Time{Now: c.now})
	e2.now = c.now
	e2.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("a restart after the claim repeated the notice: %v", sink.told)
	}
}

func TestTimeWatchWhileStoppedIsDeliveredLateOrDropped(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{answer: Ready}
	e, st, c, _ := newTimeEngine(sink)
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 10m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Subscribe(ctx, Request{Owner: "b", Source: "time", Ref: "in 10m", Lifetime: 12 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	// The Agent is down for three hours; b's watch expired meanwhile, a's did not (due + 24 h).
	c.add(3 * time.Hour)
	e2 := New(st, sink, &Time{Now: c.now})
	e2.now = c.now
	e2.Tick(ctx)
	if len(sink.told) != 1 || !strings.HasPrefix(sink.told[0], "a:") || !strings.Contains(sink.notices[0], "late") {
		t.Fatalf("told %v notices %v", sink.told, sink.notices)
	}
}

func TestTimeWatchTerms(t *testing.T) {
	ctx := context.Background()
	e, _, c, _ := newTimeEngine(&fakeSink{answer: Ready})
	var rf *ErrRefused
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1h", Until: UntilDeadline}); !errors.As(err, &rf) {
		t.Errorf("until deadline: %v", err)
	}
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 2h", Lifetime: time.Hour}); !errors.As(err, &rf) {
		t.Errorf("a lifetime that ends before the time: %v", err)
	}
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1h", Types: []string{"pr.merged"}}); !errors.As(err, &rf) {
		t.Errorf("a type the time source cannot produce: %v", err)
	}
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1h", Lifetime: MaxLifetime + time.Hour}); !errors.As(err, &rf) {
		t.Errorf("a lifetime past 30 days: %v", err)
	}
	s, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "20d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := c.t.Add(20*24*time.Hour + timeGrace); !s.Expires.Equal(want) {
		t.Errorf("a 20-day timer expires %v, want due + 24h = %v (the 7-day default would end it first)", s.Expires, want)
	}
	s, err = e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "30d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := c.t.Add(MaxLifetime); !s.Expires.Equal(want) {
		t.Errorf("expiry %v is past the 30-day cap %v", s.Expires, want)
	}
	// Every other source refuses wake: a booking for a watch that has no instant would only wake
	// the workspace for nothing.
	e2, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Ready})
	e2.SetWaker(&fakeWaker{})
	if _, err := e2.Subscribe(ctx, Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}, Wake: true}); !errors.As(err, &rf) {
		t.Errorf("wake on a non-time source: %v", err)
	}
}

func TestWakeIsBookedAndCancelledWhenTheWatchFires(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{answer: Ready}
	e, st, c, w := newTimeEngine(sink)
	sub, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 30m", Wake: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Wake || sub.WakeID == "" || !w.booked[sub.WakeID].Equal(sub.Due) {
		t.Fatalf("sub %+v booked %v", sub, w.booked)
	}
	if disk, _ := st.Load(); len(disk.Wakes) != 1 || disk.Subs[0].WakeID != sub.WakeID {
		t.Fatalf("the booking is not recorded on disk: %+v", disk)
	}
	e.Tick(ctx)
	if w.live() != 1 {
		t.Fatal("the booking was dropped before the watch fired")
	}
	c.add(31 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 1 || w.live() != 0 || len(w.cancelled) != 1 {
		t.Fatalf("told %v live %d cancelled %v: the booking must go when the watch fires", sink.told, w.live(), w.cancelled)
	}
	if disk, _ := st.Load(); len(disk.Wakes) != 0 {
		t.Fatalf("the cancelled booking is still recorded: %v", disk.Wakes)
	}
}

// The watch fires and is claimed while the Control Plane is unreachable: the booking is asked
// about again until it answers, across a restart too.
func TestWakeCancelIsRetriedUntilTheControlPlaneAnswers(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{answer: Ready}
	e, st, c, w := newTimeEngine(sink)
	sub, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 1m", Wake: true})
	if err != nil {
		t.Fatal(err)
	}
	w.cancelErr = errors.New("control plane unreachable")
	c.add(2 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 1 || w.live() != 1 {
		t.Fatalf("told %v live %d", sink.told, w.live())
	}
	if disk, _ := st.Load(); len(disk.Wakes) != 1 || disk.Wakes[0] != sub.WakeID {
		t.Fatalf("the unconfirmed booking was forgotten: %+v", disk.Wakes)
	}
	if w.tries != 1 {
		t.Fatalf("cancel asked %d times", w.tries)
	}
	e.Tick(ctx) // inside the retry gap: not asked again
	if w.tries != 1 {
		t.Fatalf("a tick inside the retry gap asked the Control Plane again (%d calls)", w.tries)
	}
	c.add(2 * time.Minute)

	// The Agent restarts, the Control Plane is back.
	w.cancelErr = nil
	e2 := New(st, sink, &Time{Now: c.now})
	e2.now = c.now
	e2.SetWaker(w)
	e2.Tick(ctx)
	if w.live() != 0 {
		t.Fatal("the booking leaked across a restart")
	}
	if disk, _ := st.Load(); len(disk.Wakes) != 0 {
		t.Fatalf("still recorded: %v", disk.Wakes)
	}
}

func TestWakeIsCancelledWhenTheWatchEndsOtherwise(t *testing.T) {
	ctx := context.Background()
	for name, end := range map[string]func(e *Engine, c *clock, sink *fakeSink, sub Subscription){
		"unwatch": func(e *Engine, _ *clock, _ *fakeSink, sub Subscription) {
			if !e.Unsubscribe("a", sub.ID) {
				t.Fatal("unwatch found nothing")
			}
		},
		"expiry": func(_ *Engine, c *clock, _ *fakeSink, _ Subscription) { c.add(48 * time.Hour) },
		"owner ended": func(_ *Engine, _ *clock, sink *fakeSink, _ Subscription) {
			sink.gone = map[string]bool{"a": true}
		},
		"owner unreachable": func(_ *Engine, _ *clock, sink *fakeSink, _ Subscription) { sink.answer = Unreachable },
	} {
		t.Run(name, func(t *testing.T) {
			sink := &fakeSink{answer: Ready}
			e, _, c, w := newTimeEngine(sink)
			sub, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m", Wake: true})
			if err != nil {
				t.Fatal(err)
			}
			if name == "owner unreachable" {
				c.add(21 * time.Minute) // the event is pending, the owner cannot take it
			}
			end(e, c, sink, sub)
			e.Tick(ctx)
			if w.live() != 0 {
				t.Fatalf("the booking outlived the watch: %v", w.booked)
			}
		})
	}
}

// A watch that ends while the Control Plane is unreachable leaves nothing behind either.
func TestWakeCancelAfterUnwatchWhileUnreachable(t *testing.T) {
	ctx := context.Background()
	e, st, c, w := newTimeEngine(&fakeSink{answer: Ready})
	sub, _ := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m", Wake: true})
	w.cancelErr = errors.New("down")
	e.Unsubscribe("a", sub.ID)
	e.Tick(ctx)
	if w.live() != 1 {
		t.Fatal("test setup: the booking should still be there")
	}
	w.cancelErr = nil
	c.add(2 * time.Minute)
	e.Tick(ctx)
	if w.live() != 0 {
		t.Fatal("the booking leaked")
	}
	if disk, _ := st.Load(); len(disk.Wakes) != 0 {
		t.Fatalf("recorded: %v", disk.Wakes)
	}
}

func TestWakeRefusals(t *testing.T) {
	ctx := context.Background()
	e, st, _, w := newTimeEngine(&fakeSink{answer: Ready})

	// The scheduler is not running: the Control Plane's refusal reaches the caller and nothing is kept.
	w.bookErr = &ErrRefused{"the Control Plane scheduler is not running"}
	var rf *ErrRefused
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m", Wake: true}); !errors.As(err, &rf) || !strings.Contains(rf.Reason, "scheduler") {
		t.Fatalf("err = %v", err)
	}
	if l := e.List("a"); len(l) != 0 {
		t.Fatalf("a refused wake left a watch: %+v", l)
	}
	// The same watch without wake is fine: it fires locally while the workspace runs.
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m"}); err != nil {
		t.Fatal(err)
	}

	// No Control Plane wired at all.
	e2 := New(&memStore{}, &fakeSink{answer: Ready}, &Time{})
	if _, err := e2.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m", Wake: true}); !errors.As(err, &rf) {
		t.Fatalf("no waker: %v", err)
	}
	_ = st
}

// A booking made for a watch that is then refused (the limit) is cancelled, not left to wake the
// workspace for nothing.
func TestWakeBookedForARefusedWatchIsCancelled(t *testing.T) {
	ctx := context.Background()
	e, _, _, w := newTimeEngine(&fakeSink{answer: Ready})
	for i := 0; i < MaxPerOwner-1; i++ {
		if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 20m"}); err != nil {
			t.Fatal(err)
		}
	}
	// The pre-check passes with one slot left, then another watch takes it while this one books.
	w2 := &racingWaker{fakeWaker: w, onBook: func() {
		if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 30m"}); err != nil {
			t.Error(err)
		}
	}}
	e.SetWaker(w2)
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 40m", Wake: true}); err == nil {
		t.Fatal("the 21st watch was accepted")
	}
	e.Tick(ctx)
	if w.live() != 0 {
		t.Fatalf("a booking for a refused watch is still there: %v", w.booked)
	}
}

type racingWaker struct {
	*fakeWaker
	onBook func()
}

func (r *racingWaker) Book(ctx context.Context, at time.Time) (string, error) {
	if r.onBook != nil {
		r.onBook()
	}
	return r.fakeWaker.Book(ctx, at)
}

// A booking made but not yet attached to its subscription is not cancelled by a tick that runs in
// between.
func TestWakeBeingBookedIsNotCancelledByReconcile(t *testing.T) {
	ctx := context.Background()
	e, _, _, w := newTimeEngine(&fakeSink{answer: Ready})
	id, err := e.bookWake(ctx, e.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.reconcileWakes(ctx)
	if w.live() != 1 {
		t.Fatal("a tick cancelled a booking whose watch was still being made")
	}
	e.endBooking(id)
	e.reconcileWakes(ctx)
	if w.live() != 0 {
		t.Fatal("an orphaned booking was kept")
	}
}

// An ordinary watch's subscription never touches the Control Plane.
func TestOrdinaryWatchesDoNotBook(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{events: []Event{ev("pr.merged", "v1")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(src, sink)
	w := &fakeWaker{}
	e.SetWaker(w)
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	e.Tick(ctx)
	if w.n != 0 || len(w.cancelled) != 0 {
		t.Fatalf("booked %d, cancelled %v", w.n, w.cancelled)
	}
}

// Once the engine has fired the watch (the event is pending, the owner busy) the booking has done
// its work and goes, even though the watch itself stays until the notice is delivered.
func TestWakeIsCancelledOnceTheEventIsPending(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{answer: Wait}
	e, _, c, w := newTimeEngine(sink)
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "time", Ref: "in 5m", Wake: true}); err != nil {
		t.Fatal(err)
	}
	c.add(6 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 0 || len(e.List("a")) != 1 {
		t.Fatalf("test setup: the notice should be waiting (told %v)", sink.told)
	}
	if w.live() != 0 {
		t.Fatal("the booking outlived the fire while the notice waited for its owner")
	}
}
