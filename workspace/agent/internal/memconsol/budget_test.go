package memconsol

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// reasons builds n distinct wrong-report reasons for one memory.
func reasons(name string, rev, n int) []memoryx.ConsolReason {
	var rs []memoryx.ConsolReason
	for i := 0; i < n; i++ {
		rs = append(rs, memoryx.ConsolReason{By: "feedback", Rule: "wrong", Note: name + " names a retired branch, report " + string(rune('a'+i)), Revision: rev})
	}
	return rs
}

func (f *fakeStore) big(name string, bodyBytes, rev, nReasons int) {
	f.put("project", testProject, memoryx.ConsolMemory{Name: name, Description: "d " + name, Type: "project", Revision: rev,
		Body: "UNIQUE-START\n" + strings.Repeat("branch line of the note\n", bodyBytes/24), Reasons: reasons(name, rev, nReasons)})
}

// promptSink records the size of every prompt a run sent, as the estimator would count it.
type promptSink struct{ tokens int }

func (p *promptSink) wrap(f func(string) (string, error)) func(string) (string, error) {
	return func(s string) (string, error) { p.tokens += estimateTokens(s) + 100; return f(s) }
}

// A topic that is cheap in fact must not be delayed because the estimate counted calls that never
// happen (a second, large-tier pass for every observation).
func TestEstimateMatchesWhatIsSent(t *testing.T) {
	f := env(t)
	f.big("notes", 30000, 1, 3)
	in0, err := Gather(t0)
	if err != nil {
		t.Fatal(err)
	}
	var priced int
	for _, tp := range BuildTopics(in0.Observations, in0.Memories) {
		priced = PlanTopic(tp, tp.Observations, DefaultConfig()).Estimate
	}
	sink := &promptSink{}
	small := &scriptModel{classify: sink.wrap(classifyAs("patch", "notes")), patch: sink.wrap(patchEdit("UNIQUE-START", "START"))}
	in := input(small, nil, t0)
	in.Config.TokenBudget = 17000
	rep := mustRun(t, in)
	if len(rep.Delayed) != 0 || rep.Status != RunOK || len(rep.Queued) != 1 {
		t.Fatalf("a topic that costs ~%d tokens in fact was held back at a budget of 17000: %+v", sink.tokens, rep)
	}
	// The estimate is never below what was sent and within a third above it. Before: 19733 for
	// this topic, about 1.35x what the same run sent (the live run: 4 topics cost 23k in total,
	// estimated 5k-77k each).
	if priced < sink.tokens || priced > sink.tokens*4/3 {
		t.Fatalf("estimate %d vs %d tokens sent", priced, sink.tokens)
	}
	// Measured on the prompts the fake runner received: the estimate is never below what was sent,
	// and within a third above it (the live run: 4 topics cost 23k, estimated 5k-77k each).
	if priced < sink.tokens || priced > sink.tokens*4/3 {
		t.Fatalf("estimate %d vs %d tokens sent", priced, sink.tokens)
	}
}

// A topic over the whole budget shrinks (fewer notes, shorter excerpts) instead of waiting forever.
func TestOverBudgetTopicShrinksToFit(t *testing.T) {
	f := env(t)
	f.big("notes", 6000, 1, 1)
	for _, n := range []string{"notes-a", "notes-b", "notes-c"} {
		f.put("project", testProject, memoryx.ConsolMemory{Name: n, Description: "d " + n, Type: "project", Revision: 1,
			Body: strings.Repeat("branch line of the note\n", 250)})
	}
	small := &scriptModel{classify: classifyAs("no-op")}
	in := input(small, nil, t0)
	in.Config.TokenBudget = 4300 // the full-size estimate is 4751, the 150-rune one 4152
	rep := mustRun(t, in)
	if len(rep.Delayed) != 0 || len(rep.TooLarge) != 0 || small.calls() != 1 {
		t.Fatalf("the topic did not shrink: calls=%d %+v", small.calls(), rep)
	}
}

// A topic that cannot fit even at the smallest size is not re-delayed run after run.
func TestImpossibleTopicIsNotDelayed(t *testing.T) {
	f := env(t)
	f.big("giant", 200000, 1, 1)
	small := &scriptModel{classify: classifyAs("patch", "giant")}
	rep := mustRun(t, input(small, nil, t0))
	if small.calls() != 0 || len(rep.Delayed) != 0 {
		t.Fatalf("calls=%d report=%+v", small.calls(), rep)
	}
}

func projMems(ms ...memoryx.ConsolMemory) map[string][]memoryx.ConsolMemory {
	return map[string][]memoryx.ConsolMemory{"project:" + testProject: ms}
}

func createObs(name, note string) Observation {
	return Observation{ID: "c-" + name, Source: SourceExport, Rule: "review-create", Scope: "project", ProjectID: testProject, Memory: name, Note: note,
		FirstSeen: t0.Add(-time.Hour), Candidate: &Candidate{Name: name, Description: "d", Type: "project", Body: "b"}}
}

// The two live mismatches: a create about a flaky test shares only common words with an
// unrelated note and must stay a topic of its own.
func TestCreateWithoutRealMatchIsItsOwnTopic(t *testing.T) {
	mems := projMems(memoryx.ConsolMemory{Name: "efs-burst-credit-starvation", Description: "EFS burst credits run out", Type: "project", Revision: 1,
		Body: "Builds slow down under load when the burst credits are gone; check the credit balance."})
	ts := BuildTopics([]Observation{createObs("opencode-contract-flaky-test", "the opencode contract test is flaky under load")}, mems)
	if len(ts) != 1 || ts[0].Memory != "opencode-contract-flaky-test" {
		t.Fatalf("topics = %+v", ts)
	}
}

// A create that really is about an existing note still joins it.
func TestCreateWithRealMatchAttaches(t *testing.T) {
	mems := projMems(memoryx.ConsolMemory{Name: "opencode-contract-test", Description: "contract tests for opencode", Type: "project", Revision: 1,
		Body: "The opencode contract test replays recorded sessions."})
	ts := BuildTopics([]Observation{createObs("opencode-contract-flaky-test", "the opencode contract test is flaky under load")}, mems)
	if len(ts) != 1 || ts[0].Memory != "opencode-contract-test" {
		t.Fatalf("topics = %+v", ts)
	}
}

// The proposal's memory is what the change is about: the created name, even from an attached topic.
func TestProposalMemoryIsTheSubjectOfTheChange(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "opencode-contract-test", Description: "contract tests for opencode", Type: "project", Revision: 1,
		Body: "The opencode contract test replays recorded sessions."})
	writeExport(t, exported(1, "create", "opencode-contract-flaky-test", "the opencode contract test is flaky under load", "flaky text", t0.Add(-48*time.Hour)))
	small := &scriptModel{classify: classifyAs("add")}
	mustRun(t, input(small, nil, t0))
	ps := pendingList(t, t0)
	if len(ps) != 1 || ps[0].Memory != "opencode-contract-flaky-test" || ps[0].Items[0].Op != OpCreate {
		t.Fatalf("proposals = %+v", ps)
	}
}

// The terminal state is visible in the run report and in the status, with the estimate, and it
// clears when the memory shrinks. It is not a delay: the run is not cut.
func TestTooLargeTopicIsReportedAndClears(t *testing.T) {
	f := env(t)
	f.big("giant", 200000, 1, 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	rep := mustRun(t, input(small, nil, t0))
	if small.calls() != 0 || len(rep.Delayed) != 0 || len(rep.TooLarge) != 1 || rep.Status != RunIdle {
		t.Fatalf("report = %+v", rep)
	}
	tl := rep.TooLarge[0]
	if tl.Cause != CauseTooLarge || tl.Memory != "giant" || tl.Estimate <= DefaultConfig().TokenBudget || tl.Detail == "" {
		t.Fatalf("too-large entry = %+v", tl)
	}
	st, err := BuildStatus(t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Waiting) != 1 || st.Waiting[0].Cause != CauseTooLarge || st.Waiting[0].Estimate != tl.Estimate {
		t.Fatalf("waiting = %+v", st.Waiting)
	}
	// The member trims the note: the same topic is taken by the next run.
	f.big("giant", 3000, 2, 1)
	rep = mustRun(t, input(&scriptModel{classify: classifyAs("no-op")}, nil, t0.Add(time.Hour)))
	if len(rep.TooLarge) != 0 || rep.Calls != 1 {
		t.Fatalf("after trimming: %+v", rep)
	}
}

// Shrinking keeps every observation it can: excerpts first, then notes, and only then observations.
func TestPlanTopicShrinkOrder(t *testing.T) {
	f := env(t)
	f.big("notes", 6000, 1, 3)
	for _, n := range []string{"notes-a", "notes-b", "notes-c"} {
		f.put("project", testProject, memoryx.ConsolMemory{Name: n, Description: "d " + n, Type: "project", Revision: 1,
			Body: strings.Repeat("branch line of the note\n", 250)})
	}
	in0, err := Gather(t0)
	if err != nil {
		t.Fatal(err)
	}
	tp := BuildTopics(in0.Observations, in0.Memories)[0]
	c := DefaultConfig()
	full := PlanTopic(tp, tp.Observations, c)
	if full.Fit != fullFit || len(full.Take) != 3 {
		t.Fatalf("a topic that fits was changed: %+v", full)
	}
	c.TokenBudget = full.Estimate - 1
	cut := PlanTopic(tp, tp.Observations, c)
	if cut.TooLarge || cut.Estimate > c.TokenBudget || len(cut.Take) != 3 || cut.Fit.Runes >= fullFit.Runes || cut.Fit.Notes != fullFit.Notes {
		t.Fatalf("one token over should only shorten the excerpts: %+v", cut)
	}
	c.TokenBudget = 4000
	tiny := PlanTopic(tp, tp.Observations, c)
	if tiny.TooLarge || tiny.Estimate > c.TokenBudget || len(tiny.Take) != 1 || tiny.Fit.Notes != 1 {
		t.Fatalf("a small budget should drop observations and notes: %+v", tiny)
	}
	c.TokenBudget = 100
	if p := PlanTopic(tp, tp.Observations, c); !p.TooLarge || len(p.Take) != 1 {
		t.Fatalf("100 tokens cannot fit anything: %+v", p)
	}
}

// The attach threshold, on both sides of each of its two conditions.
func TestAttachThresholdBoundaries(t *testing.T) {
	// "pipeline" in the name (4) + "release" in the description (2) + "runner" in the body (1) = 7: below.
	mk := func(name, desc, body string) map[string][]memoryx.ConsolMemory {
		return projMems(memoryx.ConsolMemory{Name: name, Description: desc, Type: "project", Revision: 1, Body: body})
	}
	obs := []Observation{createObs("pipeline-release-runner", "alpha bravo charlie delta echoes foxtrot golfers hotels")}
	cases := []struct {
		name   string
		mems   map[string][]memoryx.ConsolMemory
		attach bool
	}{
		{"score 7 (4+2+1)", mk("pipeline-notes", "release process", "the runner is slow"), false},
		{"score 8 (4+4)", mk("pipeline-release-notes", "other", "x"), true},
		{"score 11, none of it from the name or description", mk("unrelated-note", "other", "pipeline release runner alpha bravo charlie delta echoes foxtrot golfers hotels"), false},
		{"head 4, score 4: too low", mk("pipeline-notes", "other", "x"), false},
		{"head 6, score 8", mk("pipeline-notes", "release runner", "x"), true},
	}
	for _, tc := range cases {
		ts := BuildTopics(obs, tc.mems)
		got := ts[0].Memory != "pipeline-release-runner"
		if got != tc.attach {
			t.Errorf("%s: attached = %v, want %v (topic %s)", tc.name, got, tc.attach, ts[0].Memory)
		}
	}
}
