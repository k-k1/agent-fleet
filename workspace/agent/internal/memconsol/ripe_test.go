package memconsol

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

func ob(id, src, rule string, age time.Duration) Observation {
	return Observation{ID: id, Source: src, Rule: rule, Scope: "project", ProjectID: testProject, Memory: "m", FirstSeen: t0.Add(-age)}
}

// Decision 6: one wrong/stale report, or three new observations, or one that waited 24 h.
func TestRipeness(t *testing.T) {
	c := DefaultConfig()
	cases := []struct {
		name    string
		obs     []Observation
		pending bool
		want    bool
	}{
		{"one wrong report", []Observation{ob("1", SourceFeedback, "wrong", time.Minute)}, false, true},
		{"one stale report", []Observation{ob("1", SourceFeedback, "stale", time.Minute)}, false, true},
		{"one fresh lint reason", []Observation{ob("1", SourceLint, "missing-path", time.Hour)}, false, false},
		{"two fresh lint reasons", []Observation{ob("1", SourceLint, "a", time.Hour), ob("2", SourceLint, "b", time.Hour)}, false, false},
		{"three observations", []Observation{ob("1", SourceLint, "a", time.Minute), ob("2", SourceLint, "b", time.Minute), ob("3", SourceExport, "review-create", time.Minute)}, false, true},
		{"waited 24h", []Observation{ob("1", SourceLint, "a", 24*time.Hour)}, false, true},
		{"waited 23h", []Observation{ob("1", SourceLint, "a", 23*time.Hour)}, false, false},
		{"nothing open", nil, false, false},
		{"a proposal is already waiting", []Observation{ob("1", SourceFeedback, "wrong", time.Minute)}, true, false},
	}
	for _, tc := range cases {
		if got := Ripe(tc.obs, tc.pending, c, t0); got != tc.want {
			t.Errorf("%s: ripe = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The figures are config, not constants.
	c.NewObservations = 2
	if !Ripe([]Observation{ob("1", SourceLint, "a", time.Hour), ob("2", SourceLint, "b", time.Hour)}, false, c, t0) {
		t.Error("NewObservations is not read from the config")
	}
}

// Ripe topics are taken oldest observation first within the budget; a topic over the whole budget is
// delayed and flagged, never run half-way.
func TestSelectOrdersByOldestAndFlagsOverBudget(t *testing.T) {
	mk := func(id string, age time.Duration, est int) RipeTopic {
		return RipeTopic{Topic: Topic{ID: id, Memory: id}, Estimate: est, Oldest: t0.Add(-age)}
	}
	run, delayed := Select([]RipeTopic{mk("c", time.Hour, 400), mk("a", 3*time.Hour, 400), mk("b", 2*time.Hour, 400), mk("huge", 4*time.Hour, 5000)}, 1000)
	var ids []string
	for _, r := range run {
		ids = append(ids, r.Topic.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("run = %v", ids)
	}
	if len(delayed) != 2 || delayed[0].Topic != "huge" || delayed[0].Detail == "" || delayed[1].Topic != "c" || delayed[1].Cause != CauseBudget {
		t.Fatalf("delayed = %+v", delayed)
	}
}

// Grouping is deterministic: the same observations in any order give the same topics, in the same
// order, with the same memories.
func TestTopicsAreDeterministic(t *testing.T) {
	mems := map[string][]memoryx.ConsolMemory{"project:" + testProject: {
		{Name: "git-branches", Description: "branch rules", Body: "trunk branch is develop"},
		{Name: "ci-setup", Description: "ci", Body: "runs on every branch push"},
		{Name: "unrelated", Description: "x", Body: "zzz"},
	}}
	obs := []Observation{
		ob("1", SourceFeedback, "wrong", time.Hour),
		{ID: "2", Source: SourceExport, Rule: "review-create", Scope: "project", ProjectID: testProject, Memory: "branch-notes", Note: "the trunk branch is develop", FirstSeen: t0.Add(-2 * time.Hour)},
		{ID: "3", Source: SourceLint, Rule: "x", Scope: "project", ProjectID: testProject, Memory: "ci-setup", Note: "n", FirstSeen: t0},
	}
	obs[0].Memory = "git-branches"
	want := BuildTopics(obs, mems)
	for i := 0; i < 20; i++ {
		sh := append([]Observation(nil), obs...)
		rand.Shuffle(len(sh), func(a, b int) { sh[a], sh[b] = sh[b], sh[a] })
		if got := BuildTopics(sh, mems); !reflect.DeepEqual(got, want) {
			t.Fatalf("grouping depends on input order:\n%+v\n%+v", got, want)
		}
	}
	// The exported create that names no existing memory joined the topic of the memory it shares words with.
	var joined bool
	for _, tp := range want {
		if tp.Memory == "git-branches" && len(tp.Observations) == 2 {
			joined = true
		}
	}
	if !joined {
		t.Fatalf("topics = %+v", want)
	}
}

// Decision 7: expiry alone never re-ripens a topic; new evidence, a change to the target memory, or
// an explicit retry does.
func TestExpiryDoesNotReripenButEvidenceRevisionAndRetryDo(t *testing.T) {
	f := env(t)
	f.wrong("a", "line", 1)
	small := &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("line", "line2")}
	mustRun(t, input(small, nil, t0))
	p := pendingList(t, t0)[0]

	// Expired after 14 days: the record stays, approval stops, the topic is not ripe again.
	after := t0.Add(15 * 24 * time.Hour)
	if _, err := Approve(p.ID, after); err != ErrNotPending {
		t.Fatalf("approve after expiry: %v", err)
	}
	small2 := &scriptModel{classify: classifyAs("no-op")}
	rep := mustRun(t, input(small2, nil, after))
	if rep.Ripe != 0 || small2.calls() != 0 {
		t.Fatalf("expiry re-ripened the topic: %+v", rep)
	}
	got, err := loadProposal(p.ID)
	if err != nil || got.Status != StatusExpired || len(got.ObservationIDs) != 1 || got.TargetRevisions["a"] != 1 || len(got.Evidence) != 1 {
		t.Fatalf("expired record = %+v, %v", got, err)
	}

	// New evidence (a second report with another note) ripens it.
	f.put("project", testProject, memoryx.ConsolMemory{Name: "a", Description: "d a", Type: "project", Revision: 1, Body: "line",
		Reasons: []memoryx.ConsolReason{
			{By: "feedback", Rule: "wrong", Note: "a names a retired branch", Revision: 1},
			{By: "feedback", Rule: "stale", Note: "another report", Revision: 1}}})
	if rep := mustRun(t, input(small2, nil, after.Add(time.Hour))); rep.Ripe != 1 {
		t.Fatalf("new evidence did not ripen: %+v", rep)
	}

	// A change to the target memory reopens the old observation too.
	f.wrong("a", "line v2", 2) // same note text, new revision: a different observation id as well
	if rep := mustRun(t, input(&scriptModel{classify: classifyAs("no-op")}, nil, after.Add(2*time.Hour))); rep.Ripe != 1 {
		t.Fatalf("a revision change did not ripen: %+v", rep)
	}

	// An explicit retry reopens the observations of a decided proposal.
	f2 := env(t)
	f2.wrong("b", "text", 1)
	mustRun(t, input(&scriptModel{classify: classifyAs("patch", "b"), patch: patchEdit("text", "t2")}, nil, t0))
	pb := pendingList(t, t0)[0]
	if _, err := Reject(pb.ID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rep := mustRun(t, input(&scriptModel{classify: classifyAs("no-op")}, nil, t0.Add(2*time.Hour))); rep.Ripe != 0 {
		t.Fatalf("a rejection re-ripened the topic: %+v", rep)
	}
	if _, n, err := Retry(pb.ID, t0.Add(3*time.Hour)); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	if rep := mustRun(t, input(&scriptModel{classify: classifyAs("no-op")}, nil, t0.Add(4*time.Hour))); rep.Ripe != 1 {
		t.Fatalf("retry did not ripen: %+v", rep)
	}
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc\nd\ne\nf\ng\nh", "a\nb\nc\nd\nE\nf\ng\nh")
	want := " c\n d\n-e\n+E\n f\n g\n"
	if got != want {
		t.Fatalf("diff = %q, want %q", got, want)
	}
	if lineDiff("", "x\ny") != "+x\n+y\n" {
		t.Fatal("create diff")
	}
}
