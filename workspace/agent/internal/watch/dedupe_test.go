package watch

import (
	"context"
	"sync"
	"testing"
	"time"
)

func subReq(owner string, types ...string) Request {
	return Request{Owner: owner, Source: "fake", Ref: "o/r#1", Types: types}
}

func TestRepeatedWatchReturnsTheExistingOne(t *testing.T) {
	e, st, c := newTestEngine(&fakeSource{}, &fakeSink{answer: Ready})
	ctx := context.Background()
	first, err := e.Subscribe(ctx, subReq("a", "pr.checks.completed", "pr.merged"))
	if err != nil || first.Existing {
		t.Fatalf("first = %+v, %v", first, err)
	}
	c.add(time.Hour)
	again := subReq("a", "pr.merged", "pr.checks.completed") // same set, other order
	again.Lifetime = 300 * time.Hour
	second, err := e.Subscribe(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || !second.Existing {
		t.Fatalf("second = %+v, want existing %s", second, first.ID)
	}
	if want := c.t.Add(300 * time.Hour); !second.Expires.Equal(want) {
		t.Fatalf("expiry = %v, want the later %v", second.Expires, want)
	}
	if got := e.List("a"); len(got) != 1 || got[0].Existing {
		t.Fatalf("list = %+v, want one stored record without the flag", got)
	}
	if len(st.st.Subs) != 1 || !st.st.Subs[0].Expires.Equal(second.Expires) {
		t.Fatalf("saved = %+v", st.st.Subs)
	}
	// A shorter repeat never shortens the first one.
	short := subReq("a", "pr.merged", "pr.checks.completed")
	short.Lifetime = time.Hour
	third, _ := e.Subscribe(ctx, short)
	if !third.Expires.Equal(second.Expires) {
		t.Fatalf("expiry shortened to %v", third.Expires)
	}
}

func TestConcurrentIdenticalSubscribeMakesOneRecord(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Ready})
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := e.Subscribe(context.Background(), subReq("a", "pr.merged"))
			if err != nil {
				t.Error(err)
				return
			}
			ids <- s.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || len(e.List("a")) != 1 {
		t.Fatalf("ids = %v, records = %d", seen, len(e.List("a")))
	}
}

func TestWatchesThatDifferStaySeparate(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Ready})
	ctx := context.Background()
	base, _ := e.Subscribe(ctx, subReq("a", "pr.checks.completed"))
	other := []Request{
		subReq("a", "pr.merged"), // non-overlapping types
		subReq("b", "pr.checks.completed"),
		{Owner: "a", Source: "fake", Ref: "o/r#2", Types: []string{"pr.checks.completed"}},
		{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.completed"}, Until: UntilDeadline},
		{Owner: "b", Requester: "a", Source: "fake", Ref: "o/r#9", Types: []string{"pr.merged"}},
	}
	ids := map[string]bool{base.ID: true}
	for _, r := range other {
		s, err := e.Subscribe(ctx, r)
		if err != nil || s.Existing || ids[s.ID] {
			t.Fatalf("%+v: %+v, %v", r, s, err)
		}
		ids[s.ID] = true
	}
	if len(e.List("a"))+len(e.List("b")) != len(other)+1 {
		t.Fatal("a watch was merged")
	}
}

func TestEndedWatchIsNotRepeated(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.merged", "v1")}, hint: Hint{After: time.Minute}}
	e, _, _ := newTestEngine(src, &fakeSink{answer: Ready})
	ctx := context.Background()
	first, _ := e.Subscribe(ctx, subReq("a", "pr.merged"))
	e.Tick(ctx) // delivered, until first: gone
	second, err := e.Subscribe(ctx, subReq("a", "pr.merged"))
	if err != nil || second.Existing || second.ID == first.ID {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestTimeWatchRepeatAtTheSameDueTime(t *testing.T) {
	e, _, c, _ := newTimeEngine(&fakeSink{answer: Ready})
	ctx := context.Background()
	mk := func(ref string) Request { return Request{Owner: "a", Source: "time", Ref: ref} }
	first, err := e.Subscribe(ctx, mk("in 20m"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Subscribe(ctx, mk("in 20m"))
	if err != nil || second.ID != first.ID || !second.Existing {
		t.Fatalf("second = %+v, %v", second, err)
	}
	c.add(time.Minute)
	third, _ := e.Subscribe(ctx, mk("in 20m")) // a minute later: another instant
	if third.Existing || third.ID == first.ID {
		t.Fatalf("a different due time merged: %+v", third)
	}
}

func TestWakeRepeatCancelsItsBooking(t *testing.T) {
	e, st, _, w := newTimeEngine(&fakeSink{answer: Ready})
	ctx := context.Background()
	mk := func(wake bool) Request { return Request{Owner: "a", Source: "time", Ref: "in 20m", Wake: wake} }
	first, err := e.Subscribe(ctx, mk(true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Subscribe(ctx, mk(true))
	if err != nil || second.ID != first.ID || !second.Existing {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if w.live() != 1 || len(w.cancelled) != 1 || second.WakeID != first.WakeID || w.cancelled[0] == first.WakeID {
		t.Fatalf("live = %d, cancelled = %v, kept %s", w.live(), w.cancelled, first.WakeID)
	}
	if len(st.st.Wakes) != 1 || st.st.Wakes[0] != first.WakeID {
		t.Fatalf("wakes on disk = %v", st.st.Wakes)
	}
	// Without wake it is another watch (the first would start the workspace, this one would not).
	third, _ := e.Subscribe(ctx, mk(false))
	if third.Existing {
		t.Fatalf("wake and no wake merged")
	}
}

func TestOverlappingWatchesGiveOneNoticeAndBothEnd(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.completed", "v1")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	ctx := context.Background()
	if _, err := e.Subscribe(ctx, subReq("a", "pr.checks.completed")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Subscribe(ctx, subReq("a", "pr.checks.*")); err != nil {
		t.Fatal(err)
	}
	e.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("told = %v, want one notice", sink.told)
	}
	if got := e.List("a"); len(got) != 0 {
		t.Fatalf("until first watches left: %+v", got)
	}
	c.add(2 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("told again: %v", sink.told)
	}
}

func TestOverlappingDeadlineWatchesAdvanceTogether(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.completed", "v1")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	ctx := context.Background()
	d := func(types ...string) Request { r := subReq("a", types...); r.Until = UntilDeadline; return r }
	e.Subscribe(ctx, d("pr.checks.completed"))
	e.Subscribe(ctx, d("pr.checks.*"))
	e.Tick(ctx)
	c.add(2 * time.Minute)
	e.Tick(ctx)
	c.add(2 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("told = %v, want one notice for the version", sink.told)
	}
	src.events = []Event{ev("pr.checks.completed", "v2")} // a new version is told again, once
	c.add(2 * time.Minute)
	e.Tick(ctx)
	c.add(2 * time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 2 || len(e.List("a")) != 2 {
		t.Fatalf("told = %v, watches = %d", sink.told, len(e.List("a")))
	}
}

func TestOverlapIsNotCollapsedAcrossOwnersOrVersions(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.completed", "v1")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(src, sink)
	ctx := context.Background()
	e.Subscribe(ctx, subReq("a", "pr.checks.*"))
	// A child's notify parent watch belongs to the parent "p": another reader.
	e.Subscribe(ctx, Request{Owner: "p", Requester: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.completed"}})
	e.Tick(ctx)
	e.Tick(ctx)
	if len(sink.told) != 2 {
		t.Fatalf("told = %v, want one per owner", sink.told)
	}
}

func TestPendingOfAnotherVersionIsKept(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.completed", "v1")}, hint: Hint{After: time.Hour}}
	sink := &fakeSink{answer: Wait}
	e, _, _ := newTestEngine(src, sink)
	ctx := context.Background()
	e.Subscribe(ctx, subReq("a", "pr.checks.completed"))
	e.Subscribe(ctx, subReq("a", "pr.checks.*"))
	e.Tick(ctx) // both hold v1 pending
	// The second one's pending moves on to v2 while the first still holds v1.
	e.mu.Lock()
	e.state.Subs[1].Pending = []Event{ev("pr.checks.completed", "v2")}
	e.mu.Unlock()
	sink.answer = Ready
	e.Tick(ctx)
	if len(sink.told) != 1 || sink.told[0] != "a:pr.checks.completed:v1" {
		t.Fatalf("told = %v", sink.told)
	}
	e.Tick(ctx)
	if len(sink.told) != 2 || sink.told[1] != "a:pr.checks.completed:v2" {
		t.Fatalf("told = %v, want v2 kept for its own notice", sink.told)
	}
}

func TestFailedDeliveryReleasesTheTwins(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.completed", "v1")}, hint: Hint{After: time.Hour}}
	sink := &fakeSink{answer: Ready, injectErr: context.DeadlineExceeded}
	e, _, _ := newTestEngine(src, sink)
	ctx := context.Background()
	e.Subscribe(ctx, subReq("a", "pr.checks.completed"))
	e.Subscribe(ctx, subReq("a", "pr.checks.*"))
	e.Tick(ctx)
	if len(e.List("a")) != 2 {
		t.Fatalf("a failed delivery must keep both watches, have %d", len(e.List("a")))
	}
	sink.injectErr = nil
	e.Tick(ctx)
	if len(sink.told) != 1 || len(e.List("a")) != 0 {
		t.Fatalf("told = %v, watches = %d", sink.told, len(e.List("a")))
	}
}
