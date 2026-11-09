package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
)

const bSubject = SubjectID("fake:o/r#1")

func observe(e *Engine, evs ...Event) {
	e.ObserveBuiltin(bSubject, func(cur json.RawMessage) ([]Event, json.RawMessage, bool) { return evs, cur, true })
}

func ciSpec(owner string) BuiltinSpec {
	return BuiltinSpec{Subject: bSubject, Owner: owner, Ref: "o/r#1", Types: []string{"pr.checks.failed"}, Fallback: NotifyMember}
}

func TestBuiltinIsDeliveredOnceNeverPolledListedOrCounted(t *testing.T) {
	src := &fakeSource{}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	var specs []BuiltinSpec
	for i := 0; i < MaxPerOwner; i++ {
		sp := ciSpec("a")
		sp.Subject = SubjectID("fake:o/r#" + string(rune('A'+i)))
		specs = append(specs, sp)
	}
	specs = append(specs, ciSpec("a"))
	e.SyncBuiltins("ci", specs)
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	c.add(time.Minute)
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.failed:h1" {
		t.Fatalf("told = %v", sink.told)
	}
	if src.polls != 0 {
		t.Fatalf("a built-in's subject was polled %d times; built-ins read the caches their feature already read", src.polls)
	}
	if got := e.List("a"); len(got) != 0 {
		t.Fatalf("list_watches shows built-ins: %+v", got)
	}
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatalf("built-ins count against the session's limit: %v", err)
	}
	if e.Watching("a", bSubject, "pr.checks.failed") {
		t.Fatal("Watching counts a built-in as the session's own watch")
	}
	if e.Unsubscribe("a", BuiltinID("ci", bSubject)) {
		t.Fatal("a session could unwatch a built-in")
	}
}

func TestBuiltinBaselineTakesTheFirstSeenTerminalStateAsHistory(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	sp := BuiltinSpec{Subject: bSubject, Owner: "a", Types: []string{"pr.merged", "pr.ready"}, Notify: NotifyMember, Baseline: []string{"pr.merged"}}
	e.SyncBuiltins("n", []BuiltinSpec{sp})
	observe(e, ev("pr.merged", "t1"))
	e.Tick(context.Background())
	if len(sink.member) != 0 {
		t.Fatalf("a PR first seen merged was told: %v", sink.member)
	}
	// Another PR first seen open, merged later: news. Ready on first sight is not baselined.
	sp2 := sp
	sp2.Subject = "fake:o/r#2"
	e.SyncBuiltins("n", []BuiltinSpec{sp, sp2})
	e.ObserveBuiltin(sp2.Subject, func(c json.RawMessage) ([]Event, json.RawMessage, bool) {
		return []Event{{Subject: sp2.Subject, Type: "pr.ready", Version: "h"}}, c, true
	})
	e.Tick(context.Background())
	e.ObserveBuiltin(sp2.Subject, func(c json.RawMessage) ([]Event, json.RawMessage, bool) {
		return []Event{{Subject: sp2.Subject, Type: "pr.merged", Version: "t2"}}, c, true
	})
	e.Tick(context.Background())
	if strings.Join(sink.member, ",") != "a:pr.ready:h,a:pr.merged:t2" {
		t.Fatalf("member = %v", sink.member)
	}
	if len(sink.asked) != 0 {
		t.Fatalf("a member delivery asked the session's readiness: %v", sink.asked)
	}
}

func TestBuiltinCapAndCombine(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("review", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: []string{"pr.review.*"}, Max: 2, Combine: true}})
	observe(e, ev("pr.review.changes_requested", "1"), ev("pr.review.threads_unresolved", "1"))
	e.Tick(context.Background())
	if len(sink.told) != 2 || len(sink.notices) != 2 {
		t.Fatalf("told = %v", sink.told)
	}
	for _, v := range []string{"2", "3"} {
		c.add(time.Minute)
		observe(e, ev("pr.review.changes_requested", "1"), ev("pr.review.threads_unresolved", v))
		e.Tick(context.Background())
	}
	// One combined delivery, then one more, then the cap.
	if strings.Join(sink.told, ",") != "a:pr.review.changes_requested:1,a:pr.review.threads_unresolved:1,a:pr.review.threads_unresolved:2" {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestBuiltinFallsBackToTheMemberForAnUnreachableOwner(t *testing.T) {
	// An archived owner does not exist for a session's own watches; its built-ins stay.
	sink := &fakeSink{answer: Unreachable, gone: map[string]bool{"a": true}}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	noFallback := ciSpec("a")
	noFallback.Subject, noFallback.Fallback = "fake:o/r#2", ""
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a"), noFallback})
	observe(e, ev("pr.checks.failed", "h1"))
	e.ObserveBuiltin(noFallback.Subject, func(c json.RawMessage) ([]Event, json.RawMessage, bool) {
		return []Event{{Subject: noFallback.Subject, Type: "pr.checks.failed", Version: "h1"}}, c, true
	})
	for i := 0; i < 3; i++ {
		e.Tick(context.Background())
	}
	if strings.Join(sink.member, ",") != "a:pr.checks.failed:h1" || len(sink.told) != 0 {
		t.Fatalf("member = %v, told = %v", sink.member, sink.told)
	}
	if e.subIndexLocked(BuiltinID("ci", noFallback.Subject)) < 0 {
		t.Fatal("a built-in without a fallback was dropped instead of waiting for its feature")
	}
}

func TestRetiredBuiltinKeepsItsRecordAndAnOwnerChangeRepeatsNothing(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, st, c := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	e.SyncBuiltins("ci", nil) // switched off
	observe(e, ev("pr.checks.failed", "h2"))
	e.Tick(context.Background())
	if len(sink.told) != 1 {
		t.Fatalf("a retired built-in delivered: %v", sink.told)
	}
	// A restart, then switched on again with another holder as owner.
	e2 := New(st, sink, &fakeSource{})
	e2.now = c.now
	e2.SyncBuiltins("ci", []BuiltinSpec{ciSpec("b")})
	e2.ObserveBuiltin(bSubject, func(cur json.RawMessage) ([]Event, json.RawMessage, bool) {
		return []Event{ev("pr.checks.failed", "h1")}, cur, true
	})
	e2.Tick(context.Background())
	if len(sink.told) != 1 {
		t.Fatalf("the same head was told again after retire, restart and an owner change: %v", sink.told)
	}
	e2.SyncBuiltins("ci", nil)
	c.add(builtinRetain + time.Hour)
	e2.SyncBuiltins("other", nil)
	if len(e2.state.Retired) != 0 || len(e2.state.Cursors) != 0 {
		t.Fatalf("retired records outlive their time: %d retired, %d cursors", len(e2.state.Retired), len(e2.state.Cursors))
	}
}

func TestBuiltinPendingIsWhatHoldsNow(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	observe(e) // pushed: checks running on the new head
	sink.answer = Ready
	e.Tick(context.Background())
	if len(sink.told) != 0 {
		t.Fatalf("a failure superseded by a push was told: %v", sink.told)
	}
}

func TestBuiltinLeavesWhatTheSessionsOwnWatchTells(t *testing.T) {
	src := &fakeSource{hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.completed"}}); err != nil {
		t.Fatal(err)
	}
	// The watch reads first: a failure on h1, told and ended (until first).
	completed := ev("pr.checks.completed", "h1:fp")
	completed.Failed = true
	src.events = []Event{completed}
	e.Tick(context.Background())
	// The built-in is made, or its cache catches up, a moment later.
	c.add(time.Minute)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.completed:h1:fp" {
		t.Fatalf("told twice: %v", sink.told)
	}
	// A later head is the built-in's to tell.
	c.add(time.Minute)
	observe(e, ev("pr.checks.failed", "h2"))
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.completed:h1:fp,a:pr.checks.failed:h2" {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestBuiltinIsQuietWhileALiveWatchCoversIt(t *testing.T) {
	src := &fakeSource{hint: Hint{After: time.Hour}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.*"}, Until: UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background()) // the watch's first read: nothing yet
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	e.SyncBuiltins("ci-b", []BuiltinSpec{{Subject: bSubject, Owner: "b", Types: []string{"pr.checks.failed"}}})
	observe(e, ev("pr.checks.failed", "h1"))
	for i := 0; i < 2; i++ {
		c.add(time.Minute)
		e.Tick(context.Background())
	}
	// "b" has no watch of its own and is told; "a" hears it from its watch.
	if strings.Join(sink.told, ",") != "b:pr.checks.failed:h1" {
		t.Fatalf("told = %v", sink.told)
	}
	if s := e.state.Subs[e.subIndexLocked(BuiltinID("ci", bSubject))]; s.Told["pr.checks.failed"] != "h1" {
		t.Fatalf("the covered notice was not recorded as told, so it would come once the watch ends: %+v", s)
	}
}

func TestBuiltinSeedsApplyOnlyToANewRecord(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	sp := BuiltinSpec{Subject: bSubject, Owner: "a", Types: []string{"pr.review.*"}, Max: 3, Combine: true,
		Told: map[string]string{"pr.review.changes_requested": "1"}, Sent: 2, Cursor: json.RawMessage(`{"cr_episode":1}`)}
	e.SyncBuiltins("review", []BuiltinSpec{sp})
	if got := string(e.state.Cursors[bSubject]); got != `{"cr_episode":1}` {
		t.Fatalf("cursor seed = %s", got)
	}
	observe(e, ev("pr.review.changes_requested", "1"), ev("pr.review.threads_unresolved", "1"))
	e.Tick(context.Background())
	sp.Sent, sp.Told = 0, nil
	e.SyncBuiltins("review", []BuiltinSpec{sp})
	observe(e, ev("pr.review.changes_requested", "2"))
	e.Tick(context.Background())
	// The seeded change request is not told again; the third delivery is the last.
	if strings.Join(sink.told, ",") != "a:pr.review.threads_unresolved:1" {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestBuiltinMemberDeliveryIsRetriedAfterAFailure(t *testing.T) {
	sink := &fakeSink{notifyErr: errors.New("disk full")}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("n", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: []string{"pr.merged"}, Notify: NotifyMember}})
	observe(e) // first seen open
	observe(e, ev("pr.merged", "t"))
	e.Tick(context.Background())
	sink.notifyErr = nil
	e.Tick(context.Background())
	e.Tick(context.Background())
	if strings.Join(sink.member, ",") != "a:pr.merged:t" {
		t.Fatalf("member = %v", sink.member)
	}
}

func TestPRConditionsKeepTheFeedbackVersions(t *testing.T) {
	now := time.Now()
	var cur json.RawMessage
	step := func(pr branchpr.PR) map[string]string {
		var evs []Event
		evs, cur, _ = PRConditions("github:o/r#1", &pr, cur, now)
		out := map[string]string{}
		for _, e := range evs {
			out[e.Type] = e.Version
		}
		return out
	}
	open := branchpr.PR{State: "open", Head: "h1", Checks: "failure", ChecksPending: true}
	if got := step(open); got["pr.checks.failed"] != "" {
		t.Fatalf("a failure told while checks still run: %v", got)
	}
	open.ChecksPending = false
	open.ChangesRequested, open.UnresolvedThreads, open.Conflict = true, 2, true
	if evs, _, _ := PRConditions("github:o/r#1", &branchpr.PR{State: "open", UnresolvedThreads: 4}, nil, now); len(evs) != 1 || evs[0].Count != 4 {
		t.Fatalf("threads count not carried: %+v", evs)
	}
	got := step(open)
	if got["pr.checks.failed"] != "h1" || got["pr.review.changes_requested"] != "1" || got["pr.review.threads_unresolved"] != "1" || got["pr.conflict"] != "1" {
		t.Fatalf("got %v", got)
	}
	open.ChangesRequested, open.UnresolvedThreads, open.Conflict = false, 1, false
	step(open)
	open.ChangesRequested, open.UnresolvedThreads, open.Conflict = true, 2, true
	got = step(open)
	if got["pr.review.changes_requested"] != "2" || got["pr.review.threads_unresolved"] != "2" || got["pr.conflict"] != "2" {
		t.Fatalf("a change request, thread rise or conflict back again should be new: %v", got)
	}
	if got = step(open); got["pr.review.changes_requested"] != "2" || got["pr.review.threads_unresolved"] != "2" || got["pr.conflict"] != "2" {
		t.Fatalf("the same state seen again moved a version: %v", got)
	}
	ready := branchpr.PR{State: "open", Head: "h3", Checks: "success", Mergeable: "MERGEABLE"}
	if got := step(ready); got["pr.ready"] != "h3" {
		t.Fatalf("ready = %v", got)
	}
	if got := step(branchpr.PR{State: "merged", ClosedAt: "t1", Checks: "failure"}); len(got) != 1 || got["pr.merged"] != "t1" {
		t.Fatalf("merged = %v", got)
	}
	if got := step(branchpr.PR{State: "closed", ClosedAt: "t2"}); len(got) != 1 || got["pr.closed"] != "t2" {
		t.Fatalf("closed = %v", got)
	}
}

func TestAnUnreachableOwnersOwnWatchEndsAsForAGoneOne(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.merged", "m")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Unreachable}
	e, _, _ := newTestEngine(src, sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background())
	if n := len(e.List("a")); n != 0 || len(sink.member) != 0 || len(sink.forgot) != 1 {
		t.Fatalf("left %d watches, member %v, forgot %v", n, sink.member, sink.forgot)
	}
}

func TestASecondPurposesSeedDoesNotOverwriteTheSubjectsCursor(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	e.SyncBuiltins("a", []BuiltinSpec{{Subject: bSubject, Owner: "x", Types: []string{"pr.merged"}}})
	e.ObserveBuiltin(bSubject, func(json.RawMessage) ([]Event, json.RawMessage, bool) { return nil, json.RawMessage(`{"n":5}`), true })
	e.SyncBuiltins("b", []BuiltinSpec{{Subject: bSubject, Owner: "x", Types: []string{"pr.merged"}, Cursor: json.RawMessage(`{"n":1}`)}})
	if got := string(e.state.Cursors[bSubject]); got != `{"n":5}` {
		t.Fatalf("cursor = %s", got)
	}
}

func TestABuiltinsTypesDoNotWidenWhatAWatchedSubjectIsReadFor(t *testing.T) {
	src := &fakeSource{hint: Hint{After: time.Minute}}
	e, _, _ := newTestEngine(src, &fakeSink{answer: Wait})
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	e.SyncBuiltins("r", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: []string{"pr.review.threads_unresolved"}}})
	e.Tick(context.Background())
	if strings.Join(src.want, ",") != "pr.merged" {
		t.Fatalf("the source was asked for %v; a built-in's types would make it spend requests nobody's watch asked for", src.want)
	}
}

func threadsEv(rise string, count int) Event {
	e := ev("pr.review.threads_unresolved", rise)
	e.Count = count
	return e
}

func TestATombForAnotherHeadSurvivesAStaleObservation(t *testing.T) {
	src := &fakeSource{hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.failed"}}); err != nil {
		t.Fatal(err)
	}
	src.events = []Event{ev("pr.checks.failed", "h2:fp")}
	e.Tick(context.Background()) // the watch tells h2 and ends
	sink.answer = Wait
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1")) // the cache still shows the older head
	c.add(time.Minute)
	observe(e, ev("pr.checks.failed", "h2"))
	sink.answer = Ready
	e.Tick(context.Background())
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.failed:h2:fp" {
		t.Fatalf("told = %v", sink.told)
	}
}

// toldByWatch has a's own watch tell evs and end, leaving its tomb, then makes a built-in of
// types for a.
func toldByWatch(t *testing.T, types []string, evs ...Event) (*Engine, *fakeSink, *clock) {
	t.Helper()
	src := &fakeSource{events: evs, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, _, c := newTestEngine(src, sink)
	sub, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}})
	if err != nil {
		t.Fatal(err)
	}
	// The fake source offers neither type: widen the watch by hand.
	e.state.Subs[e.subIndexLocked(sub.ID)].Types = types
	e.Tick(context.Background())
	if len(sink.told) != 1 {
		t.Fatalf("the watch told %v", sink.told)
	}
	e.SyncBuiltins("b", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: types}})
	c.add(time.Minute)
	return e, sink, c
}

func TestAThreadsTombStandsForNoMoreThanTheCountItTold(t *testing.T) {
	threads := []string{"pr.review.threads_unresolved"}
	e, sink, _ := toldByWatch(t, threads, threadsEv("0.2", 2))
	observe(e, threadsEv("1", 2)) // the two the watch told
	e.Tick(context.Background())
	if len(sink.told) != 1 {
		t.Fatalf("told twice: %v", sink.told)
	}
	e, sink, _ = toldByWatch(t, threads, threadsEv("0.2", 2))
	observe(e, threadsEv("1", 3)) // a third is news
	e.Tick(context.Background())
	if len(sink.told) != 2 {
		t.Fatalf("a rise past what the watch told was swallowed: %v", sink.told)
	}
}

func TestAReadyTombStandsForItsHeadOnly(t *testing.T) {
	ready := []string{"pr.ready"}
	e, sink, _ := toldByWatch(t, ready, ev("pr.ready", "h1:base"))
	observe(e, ev("pr.ready", "h2"))
	e.Tick(context.Background())
	if len(sink.told) != 2 {
		t.Fatalf("ready on a new head was swallowed: %v", sink.told)
	}
}

func TestNarrowedTypesDropWhatIsPendingUnderThem(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	sp := ciSpec("a")
	sp.Types = []string{"pr.review.*"}
	e.SyncBuiltins("ci", []BuiltinSpec{sp})
	sink.answer = Ready
	e.Tick(context.Background())
	if len(sink.told) != 0 {
		t.Fatalf("told a type no longer asked for: %v", sink.told)
	}
}

func TestANegativeMaxIsNoCap(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	sp := ciSpec("a")
	sp.Max = -1
	e.SyncBuiltins("ci", []BuiltinSpec{sp})
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	if len(sink.told) != 1 {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestAFailedDeliveryUndoesOnlyItsClaim(t *testing.T) {
	sink := &fakeSink{answer: Ready, injectErr: errors.New("busy")}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	sink.onInject = func() {
		// While the injection runs: a push fails again and the PR changes hands.
		e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("b")})
		observe(e, ev("pr.checks.failed", "h2"))
	}
	e.Tick(context.Background())
	s := e.state.Subs[e.subIndexLocked(BuiltinID("ci", bSubject))]
	if s.Owner != "b" || s.Sent != 0 || s.Told["pr.checks.failed"] != "" || len(s.Pending) != 1 || s.Pending[0].Version != "h2" {
		t.Fatalf("after the failed delivery: %+v", s)
	}
	sink.onInject, sink.injectErr = nil, nil
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "b:pr.checks.failed:h2" {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestAFailedDeliveryOfARetiredBuiltinIsUndoneInItsRecord(t *testing.T) {
	sink := &fakeSink{answer: Ready, injectErr: errors.New("busy")}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	sink.onInject = func() { e.SyncBuiltins("ci", nil) }
	e.Tick(context.Background())
	sink.onInject, sink.injectErr = nil, nil
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.failed:h1" {
		t.Fatalf("a notice whose delivery failed while it was retired was lost: told = %v", sink.told)
	}
}

func TestAnOwnerChangedWhileReadinessWasAskedIsNotToldUnasked(t *testing.T) {
	sink := &fakeSink{answers: map[string]Readiness{"a": Ready, "b": Wait}}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	sink.onReady = func() { e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("b")}) }
	e.Tick(context.Background())
	if len(sink.told) != 0 {
		t.Fatalf("told %v to an owner whose readiness nobody asked", sink.told)
	}
}

func TestAnUnansweredReadIsNoFirstSight(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("n", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: []string{"pr.merged"}, Notify: NotifyMember, Baseline: []string{"pr.merged"}}})
	e.ObserveBuiltin(bSubject, func(c json.RawMessage) ([]Event, json.RawMessage, bool) {
		_, next, ok := PRConditions(bSubject, nil, c, time.Now())
		return nil, next, ok
	})
	observe(e, ev("pr.merged", "t1"))
	e.Tick(context.Background())
	if len(sink.member) != 0 {
		t.Fatalf("a PR first seen merged after an unanswered read was told: %v", sink.member)
	}
}

func TestALiveBuiltinNoSweepNamesLapses(t *testing.T) {
	e, _, c := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	e.SyncBuiltins("gone-purpose", []BuiltinSpec{ciSpec("a")})
	for i := 0; i < 3; i++ { // a purpose still swept keeps its built-ins however long
		c.add(builtinRetain / 2)
		e.SyncBuiltins("kept", []BuiltinSpec{{Subject: "fake:o/r#2", Owner: "a", Types: []string{"pr.merged"}}})
	}
	if e.subIndexLocked(BuiltinID("gone-purpose", bSubject)) >= 0 {
		t.Fatal("a built-in no sweep names any more never lapses")
	}
	if e.subIndexLocked(BuiltinID("kept", "fake:o/r#2")) < 0 {
		t.Fatal("a built-in its feature still sweeps lapsed")
	}
}

func TestAWatchTellingAnOlderStateLeavesTheBuiltinsNewerOneOwed(t *testing.T) {
	src := &fakeSource{hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Wait}
	e, _, c := newTestEngine(src, sink)
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.failed"}}); err != nil {
		t.Fatal(err)
	}
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h2")) // the cache is ahead of the watch's backed-off reads
	sink.answer = Ready
	src.events = []Event{ev("pr.checks.failed", "h1:fp")}
	e.Tick(context.Background()) // the watch tells h1 and ends
	c.add(time.Minute)
	e.Tick(context.Background())
	if strings.Join(sink.told, ",") != "a:pr.checks.failed:h1:fp,a:pr.checks.failed:h2" {
		t.Fatalf("told = %v", sink.told)
	}
}

func TestAReleasedClaimKeepsATellRecordedWhileItRan(t *testing.T) {
	sink := &fakeSink{answer: Ready, injectErr: errors.New("busy")}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	sink.onInject = func() {
		// While h1 is being injected, the session's own watch tells h2 and the cache shows it.
		e.mu.Lock()
		e.addTombLocked("a", "pr.checks.failed", ev("pr.checks.failed", "h2:fp"), e.now())
		e.mu.Unlock()
		observe(e, ev("pr.checks.failed", "h2"))
	}
	e.Tick(context.Background())
	sink.onInject, sink.injectErr = nil, nil
	observe(e, ev("pr.checks.failed", "h2"))
	e.Tick(context.Background())
	if len(sink.told) != 0 {
		t.Fatalf("h2, told by the watch, came again from the built-in: %v", sink.told)
	}
}

func TestAnEmptyPurposeRetiresNoWatch(t *testing.T) {
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	e.SyncBuiltins("", nil)
	if len(e.List("a")) != 1 {
		t.Fatal("SyncBuiltins with no purpose retired a session's own watch")
	}
}

func TestAFallingCountIsSaved(t *testing.T) {
	e, st, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Wait})
	e.SyncBuiltins("review", []BuiltinSpec{{Subject: bSubject, Owner: "a", Types: []string{"pr.review.*"}}})
	observe(e, threadsEv("1", 3))
	observe(e, threadsEv("1", 2))
	if p := st.st.Subs[0].Pending; len(p) != 1 || p[0].Count != 2 {
		t.Fatalf("on disk: %+v", p)
	}
}

func TestARouteChangedWhileReadinessWasAskedIsNotDeliveredTheOldWay(t *testing.T) {
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	sp := ciSpec("a")
	e.SyncBuiltins("ci", []BuiltinSpec{sp})
	observe(e, ev("pr.checks.failed", "h1"))
	sink.onReady = func() {
		sp.Notify = NotifyMember
		e.SyncBuiltins("ci", []BuiltinSpec{sp})
	}
	e.Tick(context.Background())
	if len(sink.told) != 0 {
		t.Fatalf("injected into the session after the route moved to the member: %v", sink.told)
	}
	sink.onReady = nil
	e.Tick(context.Background())
	if strings.Join(sink.member, ",") != "a:pr.checks.failed:h1" {
		t.Fatalf("member = %v", sink.member)
	}
}

func TestAClosedPRsLaterUpdatesAreNotANewClose(t *testing.T) {
	now := time.Now()
	var cur json.RawMessage
	version := func(pr branchpr.PR) string {
		evs, next, _ := PRConditions("bitbucket:w/r#1", &pr, cur, now)
		cur = next
		for _, e := range evs {
			if e.Type == "pr.merged" || e.Type == "pr.closed" {
				return e.Type + "@" + e.Version
			}
		}
		return ""
	}
	// Bitbucket reports its last update as the close time: a comment after the decline moves it.
	got := []string{
		version(branchpr.PR{State: "closed", ClosedAt: "u1"}),
		version(branchpr.PR{State: "closed", ClosedAt: "u2"}),
		version(branchpr.PR{State: "open"}),
		version(branchpr.PR{State: "closed", ClosedAt: "u3"}),
		version(branchpr.PR{State: "merged", ClosedAt: "u4"}),
		version(branchpr.PR{State: "merged", ClosedAt: "u5"}),
	}
	if strings.Join(got, ",") != "pr.closed@u1,pr.closed@u1,,pr.closed@u3,pr.merged@u4,pr.merged@u4" {
		t.Fatalf("versions = %v", got)
	}
}

func TestAMemberBuiltinLeavingTheSetDeliversWhatItOwesFirst(t *testing.T) {
	sink := &fakeSink{notifyErr: errors.New("outbox down")}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	sp := BuiltinSpec{Subject: bSubject, Owner: "a", Types: []string{"pr.merged"}, Notify: NotifyMember}
	e.SyncBuiltins("n", []BuiltinSpec{sp})
	observe(e) // first seen open
	observe(e, ev("pr.merged", "t"))
	e.Tick(context.Background()) // the outbox fails
	e.SyncBuiltins("n", nil)     // the session was archived meanwhile
	sink.notifyErr = nil
	e.Tick(context.Background())
	e.SyncBuiltins("n", nil)
	e.Tick(context.Background())
	if strings.Join(sink.member, ",") != "a:pr.merged:t" {
		t.Fatalf("member = %v", sink.member)
	}
	if e.subIndexLocked(BuiltinID("n", bSubject)) >= 0 {
		t.Fatal("still live once delivered and out of the set")
	}
	if _, ok := e.state.Retired[BuiltinID("n", bSubject)]; !ok {
		t.Fatal("not retired with its record once delivered")
	}
}

func TestASessionBuiltinLeavingTheSetOwesNothingMore(t *testing.T) {
	sink := &fakeSink{answer: Wait}
	e, _, _ := newTestEngine(&fakeSource{}, sink)
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	observe(e, ev("pr.checks.failed", "h1"))
	e.SyncBuiltins("ci", nil) // the session no longer holds the PR
	sink.answer = Ready
	e.Tick(context.Background())
	if len(sink.told) != 0 || e.subIndexLocked(BuiltinID("ci", bSubject)) >= 0 {
		t.Fatalf("a session no longer holding the PR was told %v", sink.told)
	}
}
