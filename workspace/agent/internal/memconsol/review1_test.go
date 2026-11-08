package memconsol

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// twoItems queues a proposal with two patches (two memories of one topic).
func twoItems(t *testing.T, f *fakeStore) Proposal {
	t.Helper()
	f.wrong("alpha", "alpha one\nbeta shared", 1)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "beta", Description: "d", Type: "project", Revision: 1, Body: "beta two shared"})
	p := Proposal{ID: newID(), TopicID: "t", Scope: "project", ProjectID: testProject, Memory: "alpha", Status: StatusPending,
		Created: t0, Expires: t0.Add(14 * 24 * time.Hour),
		Items: []Item{
			{Op: OpPatch, Name: "alpha", Revision: 1, Edits: []Edit{{Old: "alpha one", New: "alpha 1"}}},
			{Op: OpPatch, Name: "beta", Revision: 1, Edits: []Edit{{Old: "beta two", New: "beta 2"}}}}}
	if err := saveProposal(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A cut-off approval (the second item's publish hit a store error after the first went out) is
// finished by approving again: the first item is found by its commit and not re-validated against
// its own write, and nothing is written twice.
func TestApproveResumesAfterAStoreErrorMidway(t *testing.T) {
	f := env(t)
	p := twoItems(t, f)
	f.failAt = 2
	if _, err := Approve(p.ID, t0.Add(time.Hour)); err == nil {
		t.Fatal("the store error was swallowed")
	}
	got, _ := loadProposal(p.ID)
	if got.Status != StatusPending || got.Items[0].Commit == "" || got.Items[1].Commit != "" {
		t.Fatalf("after the error: %+v", got)
	}
	done, err := Approve(p.ID, t0.Add(2*time.Hour))
	if err != nil || done.Status != StatusApproved || f.nPublished() != 2 {
		t.Fatalf("resume = %+v, %v, published %d", done, err, f.nPublished())
	}
}

// The same when the stop came before the proposal file recorded the first commit: the trailer
// lookup finds it.
func TestApproveFindsAnItemPublishedButNotRecorded(t *testing.T) {
	f := env(t)
	p := twoItems(t, f)
	if _, err := f.Publish(publishID(p.ID, 0), memoryx.ConsolItem{Op: "patch", Scope: "project", ProjectID: testProject, Name: "alpha", Revision: 1}, t0); err != nil {
		t.Fatal(err)
	}
	done, err := Approve(p.ID, t0.Add(time.Hour))
	if err != nil || done.Status != StatusApproved || f.nPublished() != 2 {
		t.Fatalf("approve = %+v, %v, published %d", done, err, f.nPublished())
	}
}

// A run that is cut, or fails, still reports the tokens its calls spent.
func TestCutAndFailedRunsReportTheirTokens(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("x", "y")}
	on := true
	small.after = func(int) { on = false }
	in := input(small, nil, t0)
	in.Enabled = func() bool { return on }
	if rep := mustRun(t, in); rep.Status != RunCut || rep.Calls != 1 || rep.Tokens != 1100 {
		t.Fatalf("cut report = %+v", rep)
	}
	f2 := env(t)
	f2.wrong("a", "x", 1)
	bad := &scriptModel{classify: classifyAs("patch", "a"), patch: func(string) (string, error) { return "", context.DeadlineExceeded }}
	if rep := mustRun(t, input(bad, nil, t0)); rep.Status != RunFailed || rep.Calls != 2 || rep.Tokens <= 1100 {
		t.Fatalf("failed report = %+v", rep)
	}
}

// An observation the engine gave up on is looked at again once its memory has changed. An exported
// proposal's id carries no revision, so this is the path where the revision is the only signal.
func TestDroppedObservationReopensWhenTheMemoryChanges(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "a", Description: "d", Type: "project", Revision: 1, Body: "x"})
	writeExport(t, exported(1, "update", "a", "old idea", "new body", t0.Add(-48*time.Hour)))
	bad := &scriptModel{classify: func(string) (string, error) { return `{"operation":"patch","targets":["zz"],"reason":"r"}`, nil }}
	for i := 0; i < 3; i++ {
		mustRun(t, input(bad, nil, t0.Add(time.Duration(i)*time.Hour)))
	}
	if bad.calls() != 2 {
		t.Fatalf("calls = %d, want 2 (then given up)", bad.calls())
	}
	f.put("project", testProject, memoryx.ConsolMemory{Name: "a", Description: "d", Type: "project", Revision: 2, Body: "x2"})
	good := &scriptModel{classify: classifyAs("no-op")}
	mustRun(t, input(good, nil, t0.Add(5*time.Hour)))
	if good.calls() != 1 {
		t.Fatalf("a changed memory was not looked at again (calls %d)", good.calls())
	}
}

// Retry of an expired proposal stores the expiry it applied.
func TestRetryStoresTheExpiryItFlips(t *testing.T) {
	f := env(t)
	p := queued(t, f)
	later := t0.Add(20 * 24 * time.Hour)
	if _, _, err := Retry(p.ID, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadProposal(p.ID); got.Status != StatusExpired {
		t.Fatalf("stored status = %s", got.Status)
	}
}

// A first sighting is recorded between runs, so a wait of 24 h shows in the status although no run
// has been possible.
func TestStatusCountsTheWaitFromTheFirstSighting(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "a", Description: "d", Type: "project", Revision: 1, Body: "x",
		Reasons: []memoryx.ConsolReason{{By: "lint", Rule: "missing-path", Note: "n", Revision: 1}}})
	enabled, busy, last := true, true, t0
	tickEnv(t, &enabled, &busy, &last, &scriptModel{}, nil)
	Tick(context.Background(), t0)
	if st, _ := BuildStatus(t0.Add(time.Hour)); len(st.Waiting) != 0 {
		t.Fatalf("ripe too early: %+v", st.Waiting)
	}
	Tick(context.Background(), t0.Add(25*time.Hour))
	st, err := BuildStatus(t0.Add(25 * time.Hour))
	if err != nil || len(st.Waiting) != 1 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// Switched off, a tick writes nothing.
func TestOffTickWritesNothing(t *testing.T) {
	env(t)
	enabled, busy, last := false, false, time.Time{}
	tickEnv(t, &enabled, &busy, &last, &scriptModel{}, nil)
	Tick(context.Background(), t0)
	if _, err := statStateFile(); err == nil {
		t.Fatal("an off tick wrote state.json")
	}
}
