package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
)

func ghRunsBody(runs ...map[string]any) map[string]any {
	list := make([]any, 0, len(runs))
	for _, r := range runs {
		list = append(list, r)
	}
	return map[string]any{"total_count": len(list), "check_runs": list}
}

func fixRun(id int, name, status, conclusion string) map[string]any {
	return map[string]any{"id": id, "name": name, "status": status, "conclusion": conclusion, "completed_at": "t" + name}
}

func TestGitHubTellsTheFirstFailureWhileOthersRun(t *testing.T) {
	f := newFakeGitHub()
	f.bodies[pullPath] = openPull(boolp(true), "unstable")
	f.bodies[runsPath] = ghRunsBody(fixRun(1, "console", "completed", "failure"), fixRun(2, "e2e", "in_progress", ""))
	f.bodies[statusPath] = map[string]any{"statuses": []any{}}
	f.bodies[reviewsPath] = []any{}
	g := newTestGitHub(t, f)
	evs, cur, hint, err := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := types(evs); got != "pr.checks.failing,pr.mergeable" {
		t.Fatalf("one failed, one running: %s", got)
	}
	if evs[0].Version != "abc1234def" || len(evs[0].Data) != 1 || evs[0].Data[0].Text != "console" {
		t.Fatalf("failing event = %+v", evs[0])
	}
	if hint.After != ghBusyAfter {
		t.Fatalf("running checks should poll fast: %v", hint)
	}

	// A second failure on the same head, still running: the same version, which the engine
	// does not tell twice.
	f.bodies[runsPath] = ghRunsBody(fixRun(1, "console", "completed", "failure"), fixRun(2, "e2e", "completed", "failure"), fixRun(3, "docs", "queued", ""))
	evs2, cur, _, _ := g.Poll(context.Background(), "github:o/r#7", cur, nil)
	if evs2[0].Type != "pr.checks.failing" || evs2[0].Version != evs[0].Version {
		t.Fatalf("a second failure moved the version: %+v", evs2)
	}

	// Everything finished: failed and completed as before, no failing.
	f.bodies[runsPath] = ghRunsBody(fixRun(1, "console", "completed", "failure"), fixRun(2, "e2e", "completed", "failure"), fixRun(3, "docs", "completed", "success"))
	evs3, _, _, _ := g.Poll(context.Background(), "github:o/r#7", cur, nil)
	if got := types(evs3); got != "pr.checks.completed,pr.checks.failed,pr.mergeable" {
		t.Fatalf("all finished: %s", got)
	}

	// Only running or passed checks: nothing.
	f.bodies[runsPath] = ghRunsBody(fixRun(1, "console", "completed", "success"), fixRun(2, "e2e", "in_progress", ""))
	evs4, _, _, _ := g.Poll(context.Background(), "github:o/r#7", nil, nil)
	if strings.Contains(types(evs4), "pr.checks.f") {
		t.Fatalf("no failure yet: %s", types(evs4))
	}
}

func TestBitbucketTellsTheFirstFailureWhileOthersRun(t *testing.T) {
	pr := bbOpen()
	pr.Checks, pr.ChecksPending, pr.ChecksDigest = "failure", true, "d1"
	pr.FailingChecks = []branchpr.Check{{Name: "unit", URL: "https://ci.test/1"}}
	b := bbSource(&pr)
	evs, cur, _, err := b.Poll(context.Background(), "bitbucket:w/r#4", nil, nil)
	if err != nil || types(evs) != "pr.checks.failing,pr.mergeable" || evs[0].Version != "c0ffee4" || evs[0].Data[0].Text != "unit" {
		t.Fatalf("failing: %v %s %+v", err, types(evs), evs)
	}
	pr.ChecksPending, pr.ChecksDigest = false, "d2"
	evs, _, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if got := types(evs); got != "pr.checks.completed,pr.checks.failed,pr.mergeable" {
		t.Fatalf("all finished: %s", got)
	}
}

func TestPRConditionsFailingIsNotTheFeedbackNotice(t *testing.T) {
	now := time.Now()
	pr := branchpr.PR{State: "open", Head: "h1", Checks: "failure", ChecksPending: true,
		FailingChecks: []branchpr.Check{{Name: "console"}}}
	evs, cur, _ := PRConditions("github:o/r#1", &pr, nil, now)
	if got := types(evs); got != "pr.checks.failing" || evs[0].Version != "h1" {
		t.Fatalf("running: %s", got)
	}
	pr.FailingChecks = nil
	if evs, _, _ = PRConditions("github:o/r#1", &pr, cur, now); len(evs) != 0 {
		t.Fatalf("a failure with no name quoted nothing to tell: %s", types(evs))
	}
	pr.ChecksPending, pr.FailingChecks = false, []branchpr.Check{{Name: "console"}}
	if evs, _, _ = PRConditions("github:o/r#1", &pr, cur, now); types(evs) != "pr.checks.failed" {
		t.Fatalf("finished: %s", types(evs))
	}

	// The built-in PR feedback asks for pr.checks.failed by name, and no pattern a built-in
	// uses reaches failing.
	e, _, _ := newTestEngine(&fakeSource{}, &fakeSink{answer: Ready})
	e.SyncBuiltins("ci", []BuiltinSpec{ciSpec("a")})
	pr.ChecksPending = true
	pr.FailingChecks = []branchpr.Check{{Name: "console"}}
	e.ObserveBuiltin(bSubject, func(cur json.RawMessage) ([]Event, json.RawMessage, bool) {
		return PRConditions(bSubject, &pr, cur, now)
	})
	for _, s := range e.state.Subs {
		if len(s.Pending) != 0 {
			t.Fatalf("the PR feedback built-in was handed %+v", s.Pending)
		}
	}
}

func TestFailingIsToldOncePerHeadAndNotRepeatedAfterARestart(t *testing.T) {
	src := &fakeSource{events: []Event{ev("pr.checks.failing", "h1")}, hint: Hint{After: time.Minute}}
	sink := &fakeSink{answer: Ready}
	e, st, c := newTestEngine(src, sink)
	ctx := context.Background()
	if _, err := e.Subscribe(ctx, Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.*"}, Until: UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	e.Tick(ctx)
	c.add(time.Minute)
	e.Tick(ctx)
	if len(sink.told) != 1 || sink.told[0] != "a:pr.checks.failing:h1" {
		t.Fatalf("told = %v", sink.told)
	}
	// A restart over the same store: the head already told is not told again.
	e2 := New(st, sink, src)
	e2.now = c.now
	c.add(time.Minute)
	e2.Tick(ctx)
	if len(sink.told) != 1 {
		t.Fatalf("a restart repeated the notice: %v", sink.told)
	}
	// A new head is news again.
	src.events = []Event{ev("pr.checks.failing", "h2")}
	c.add(time.Minute)
	e2.Tick(ctx)
	if len(sink.told) != 2 || sink.told[1] != "a:pr.checks.failing:h2" {
		t.Fatalf("a new head: %v", sink.told)
	}
	// By name too.
	if _, err := e2.Subscribe(ctx, Request{Owner: "b", Source: "fake", Ref: "o/r#1", Types: []string{"pr.checks.failing"}, Until: UntilDeadline}); err != nil {
		t.Fatal(err)
	}
}
