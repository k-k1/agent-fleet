package memconsol

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// A wrong report makes a topic ripe; the run classifies it (small), asks for the edit (small),
// validates and queues one pending proposal with the observation ids, the target revision and the
// evidence. Nothing reaches the store.
func TestRunQueuesAPatchProposalAndNeverWritesTheStore(t *testing.T) {
	f := env(t)
	f.wrong("branches", "trunk is develop\nrest", 3)
	small := &scriptModel{classify: classifyAs("patch", "branches"), patch: patchEdit("trunk is develop", "trunk is main")}
	rep := mustRun(t, input(small, nil, t0))
	if rep.Status != RunOK || len(rep.Queued) != 1 || rep.Calls != 2 || rep.Tokens != 2200 {
		t.Fatalf("report = %+v", rep)
	}
	ps := pendingList(t, t0)
	if len(ps) != 1 {
		t.Fatalf("pending = %d", len(ps))
	}
	p := ps[0]
	if p.Memory != "branches" || len(p.Items) != 1 || p.Items[0].Op != OpPatch || p.Items[0].Revision != 3 ||
		len(p.ObservationIDs) != 1 || len(p.Evidence) != 1 || p.Evidence[0].Rule != "wrong" || p.TargetRevisions["branches"] != 3 {
		t.Fatalf("proposal = %+v", p)
	}
	if !p.Expires.Equal(t0.Add(14 * 24 * time.Hour)) {
		t.Errorf("expires = %v", p.Expires)
	}
	if f.nPublished() != 0 {
		t.Fatal("a run wrote the store")
	}
	// The observation is covered: the next run finds nothing ripe and makes no call.
	small2 := &scriptModel{}
	if rep := mustRun(t, input(small2, nil, t0.Add(time.Hour))); rep.Status != RunIdle || small2.calls() != 0 {
		t.Fatalf("second run = %+v, calls %d", rep, small2.calls())
	}
}

// Supersede and merge escalate to the large tier; the large answer stands, and with no call that
// writes a replacement it reaches the member as advice about one target.
func TestSupersedeEscalatesAndQueuesAdviceOnly(t *testing.T) {
	f := env(t)
	f.wrong("old-note", "all stale", 1)
	small := &scriptModel{classify: classifyAs("supersede", "old-note")}
	large := &scriptModel{classify: classifyAs("supersede", "old-note")}
	rep := mustRun(t, input(small, large, t0))
	if small.nClass != 1 || large.nClass != 1 || rep.Status != RunOK {
		t.Fatalf("calls small=%d large=%d report=%+v", small.nClass, large.nClass, rep)
	}
	it := pendingList(t, t0)[0].Items[0]
	if it.Op != OpNote || it.Advice != "supersede" || it.Name != "old-note" || it.Role != "large" {
		t.Fatalf("item = %+v", it)
	}
	if _, err := Approve(pendingList(t, t0)[0].ID, t0); err == nil || f.nPublished() != 0 {
		t.Fatalf("an advice-only proposal was approvable: %v", err)
	}
}

// A model or runner error (a retired model's command failing) fails the run: nothing is queued from
// the failed call, no later topic is tried, no fallback model is used, and the topic stays ripe.
func TestModelErrorFailsTheRunSafely(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "y", 1)
	small := &scriptModel{classify: func(string) (string, error) { return "", errors.New("model retired") }}
	large := &scriptModel{classify: classifyAs("no-op")}
	rep := mustRun(t, input(small, large, t0))
	if rep.Status != RunFailed || rep.Error == "" || len(rep.Queued) != 0 || small.calls() != 1 || large.calls() != 0 {
		t.Fatalf("report = %+v, small=%d large=%d", rep, small.calls(), large.calls())
	}
	if strings.Contains(rep.Error, "retired") {
		t.Error("the runner's own text leaked into the report")
	}
	if len(pendingList(t, t0)) != 0 || len(stateOf(t).Observations) == 0 {
		t.Fatal("state after a failed run is wrong")
	}
	for _, o := range stateOf(t).Observations {
		if o.Covered != nil {
			t.Fatal("a failed call covered an observation")
		}
	}
	if len(rep.Delayed) != 2 || rep.Delayed[0].Cause != CauseRunFailed {
		t.Fatalf("delayed = %+v", rep.Delayed)
	}
}

// An answer that fails validation is dropped, counted and never retried with another call; after
// two drops the observation is left alone.
func TestInvalidAnswerIsDroppedNotRetried(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: func(string) (string, error) { return `{"operation":"patch","targets":["not-shown"],"reason":"r"}`, nil }}
	rep := mustRun(t, input(small, nil, t0))
	if small.calls() != 1 || rep.Dropped != 1 || len(rep.Queued) != 0 {
		t.Fatalf("calls=%d report=%+v", small.calls(), rep)
	}
	mustRun(t, input(small, nil, t0.Add(time.Hour)))
	if small.calls() != 2 {
		t.Fatalf("second run calls = %d", small.calls())
	}
	mustRun(t, input(small, nil, t0.Add(2*time.Hour)))
	if small.calls() != 2 {
		t.Fatalf("a third call was made for an observation the engine gave up on (calls = %d)", small.calls())
	}
}

// The patch the model returns is checked against the memory: an edit whose old text is not in it is
// refused by the engine's own validation, and nothing is queued.
func TestPatchThatDoesNotApplyQueuesNothing(t *testing.T) {
	f := env(t)
	f.wrong("a", "line one", 1)
	small := &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("text that is not there", "x")}
	rep := mustRun(t, input(small, nil, t0))
	if len(pendingList(t, t0)) != 0 || rep.Dropped != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// Decision 6: the budget takes topics oldest observation first; the rest are delayed with the cause.
func TestBudgetDelaysTheRestWithTheCause(t *testing.T) {
	f := env(t)
	f.wrong("young", "x", 1)
	f.wrong("old", "y", 1)
	// The observation of "old" was first seen earlier.
	small := &scriptModel{classify: classifyAs("no-op")}
	in := input(small, nil, t0)
	mustRun(t, in) // stamps both first sightings at t0 and handles both (no-op)
	// New evidence on both, "old" first.
	f.put("project", testProject, memoryx.ConsolMemory{Name: "old", Description: "d", Type: "project", Revision: 2, Body: "y2",
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "stale", Note: "again", Revision: 2}}})
	f.put("project", testProject, memoryx.ConsolMemory{Name: "young", Description: "d", Type: "project", Revision: 2, Body: "x2",
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "stale", Note: "again", Revision: 2}}})
	later := t0.Add(time.Hour)
	small2 := &scriptModel{classify: classifyAs("no-op")}
	in2 := input(small2, nil, later)
	in2.Config.TokenBudget = 4000 // one topic's estimate (~3350) fits, two do not
	rep := mustRun(t, in2)
	if rep.Ripe != 2 || len(rep.Handled) != 1 || len(rep.Delayed) != 1 || rep.Delayed[0].Cause != CauseBudget {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Status != RunOK && rep.Status != RunCut {
		t.Fatalf("status = %s", rep.Status)
	}
}

// Decision 6: when a turn starts during a run, the call in flight finishes and the rest is deferred;
// nothing is queued from the topic that was cut.
func TestATurnStartingDefersTheRest(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "y", 1)
	var small *scriptModel
	small = &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("x", "z")}
	flipped := false
	small.after = func(n int) { flipped = n >= 1 } // the first call is in flight when a turn starts
	in := input(small, nil, t0)
	in.Turns = fakeTurns{started: func() bool { return flipped }}
	rep := mustRun(t, in)
	if small.calls() != 1 {
		t.Fatalf("calls = %d: a call was started after the turn began", small.calls())
	}
	if rep.Status != RunCut || len(rep.Queued) != 0 || len(rep.Delayed) != 2 || rep.Delayed[0].Cause != CauseInterrupted {
		t.Fatalf("report = %+v", rep)
	}
}

// The member's switch is asked before every call: once it is off no further call starts.
func TestSwitchingOffStopsFurtherCalls(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("x", "z")}
	on := true
	small.after = func(int) { on = false }
	in := input(small, nil, t0)
	in.Enabled = func() bool { return on }
	rep := mustRun(t, in)
	if small.calls() != 1 || rep.Status != RunCut || len(pendingList(t, t0)) != 0 {
		t.Fatalf("calls=%d report=%+v", small.calls(), rep)
	}
}

// An exported create over a forgotten name is refused (no revival), and the 0117 export file is
// never modified by a run.
func TestNoRevivalAndTheExportIsReadOnly(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "anchor", Description: "d", Type: "project", Revision: 1, Body: "b"})
	f.forgotten["project:"+testProject+"/gone"] = true
	f.forgotten["project:"+testProject+"/also-gone"] = true
	raw := writeExport(t,
		exported(1, "create", "gone", "reason one", "revived text", t0.Add(-48*time.Hour)),
		exported(2, "create", "fresh", "reason two", "fresh text", t0.Add(-47*time.Hour)))
	fi, _ := os.Stat(memreviewExport())
	small := &scriptModel{classify: classifyAs("add")}
	rep := mustRun(t, input(small, nil, t0))
	ps := pendingList(t, t0)
	creates := map[string]bool{}
	for _, p := range ps {
		for _, it := range p.Items {
			if it.Op == OpCreate {
				creates[it.Name] = true
			}
		}
	}
	if creates["gone"] || !creates["fresh"] {
		t.Fatalf("creates = %v, report = %+v", creates, rep)
	}
	if rep.Rejected != 1 {
		t.Errorf("rejected = %d", rep.Rejected)
	}
	after, err := os.ReadFile(memreviewExport())
	if err != nil || string(after) != string(raw) {
		t.Fatal("the export file was rewritten")
	}
	if fi2, _ := os.Stat(memreviewExport()); !fi2.ModTime().Equal(fi.ModTime()) {
		t.Fatal("the export file was touched")
	}
	if f.nPublished() != 0 {
		t.Fatal("a run wrote the store")
	}
}

// A secret in a note keeps the observation out of every prompt: it is skipped whole.
func TestAnObservationWithASecretIsNeverSent(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "a", Description: "d", Type: "project", Revision: 1, Body: "x",
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "wrong", Note: "key " + "AKIA" + "ABCDEFGHIJKLMNOP is wrong", Revision: 1}}})
	small := &scriptModel{classify: classifyAs("no-op")}
	rep := mustRun(t, input(small, nil, t0))
	if small.calls() != 0 || rep.Skipped != 1 {
		t.Fatalf("calls=%d report=%+v", small.calls(), rep)
	}
}

// A cancelled context stops before the next call.
func TestRunStopsOnShutdown(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := Run(ctx, input(small, nil, t0))
	if err != nil || small.calls() != 0 || rep.Status != RunCut {
		t.Fatalf("err=%v calls=%d report=%+v", err, small.calls(), rep)
	}
}

func memreviewExport() string {
	return exportPathForTest()
}
