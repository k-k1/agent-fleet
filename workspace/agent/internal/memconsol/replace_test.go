package memconsol

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

func (f *fakeStore) plain(name, body string, rev int) {
	f.put("project", testProject, memoryx.ConsolMemory{Name: name, Description: "d " + name, Type: "project", Revision: rev, Body: body})
}

// queuedSupersede runs one topic whose large tier answers supersede and composes the replacement.
func queuedSupersede(t *testing.T, f *fakeStore) Proposal {
	t.Helper()
	f.wrong("old-note", "all stale", 2)
	small := &scriptModel{classify: classifyAs("supersede", "old-note")}
	large := &scriptModel{classify: classifyAs("supersede", "old-note"), compose: composeAs("new-note", "all fresh")}
	rep := mustRun(t, input(small, large, t0))
	if rep.Status != RunOK || len(rep.Queued) != 1 || large.nCompose != 1 {
		t.Fatalf("report = %+v, compose calls %d", rep, large.nCompose)
	}
	return pendingList(t, t0)[0]
}

// A supersede escalates to the large tier, which also writes the replacement; the proposal holds one
// replace item with the revision of the memory it supersedes, and approving it publishes that one item.
func TestSupersedeQueuesAReplaceAndApprovePublishesIt(t *testing.T) {
	f := env(t)
	p := queuedSupersede(t, f)
	if len(p.Items) != 1 {
		t.Fatalf("items = %+v", p.Items)
	}
	it := p.Items[0]
	if it.Op != OpReplace || it.Name != "new-note" || it.Body != "all fresh" || it.Role != "large" ||
		len(it.Sources) != 1 || it.Sources[0] != (Source{"old-note", 2}) {
		t.Fatalf("item = %+v", it)
	}
	if f.nPublished() != 0 {
		t.Fatal("a run wrote the store")
	}
	v, err := View(p.ID, t0)
	if err != nil || v.Views[0].Stale != "" || !strings.Contains(v.Views[0].Diff, "+all fresh") {
		t.Fatalf("view = %+v, %v", v.Views, err)
	}
	got, err := Approve(p.ID, t0.Add(time.Hour))
	if err != nil || got.Status != StatusApproved || got.Items[0].Commit == "" {
		t.Fatalf("approve = %+v, %v", got, err)
	}
	if len(f.published) != 1 || f.published[0].Op != "replace" || f.published[0].Name != "new-note" ||
		len(f.published[0].Sources) != 1 || f.published[0].Sources[0] != (memoryx.ConsolSource{Name: "old-note", Revision: 2}) {
		t.Fatalf("published = %+v", f.published)
	}
}

// A merge of three memories becomes one replace over all three.
func TestMergeOfThreeQueuesOneReplace(t *testing.T) {
	f := env(t)
	f.wrong("rule-a", "use make build", 1)
	f.plain("rule-b", "use make build please", 4)
	f.plain("rule-c", "use make build always", 7)
	small := &scriptModel{classify: classifyAs("merge", "rule-a", "rule-b", "rule-c")}
	large := &scriptModel{classify: classifyAs("merge", "rule-a", "rule-b", "rule-c"), compose: composeAs("build-rule", "use make build")}
	if rep := mustRun(t, input(small, large, t0)); rep.Status != RunOK {
		t.Fatalf("report = %+v", rep)
	}
	it := pendingList(t, t0)[0].Items[0]
	want := []Source{{"rule-a", 1}, {"rule-b", 4}, {"rule-c", 7}}
	if it.Op != OpReplace || len(it.Sources) != 3 || it.Sources[0] != want[0] || it.Sources[1] != want[1] || it.Sources[2] != want[2] {
		t.Fatalf("item = %+v", it)
	}
	if !strings.Contains(large.lastPrompt(), "### rule-b") {
		t.Error("the compose call did not show the merged notes in full")
	}
}

// An add with no exported candidate is written by the large tier and queued as a create.
func TestAddWithoutACandidateQueuesACreate(t *testing.T) {
	f := env(t)
	f.wrong("branches", "trunk is main", 1)
	small := &scriptModel{classify: classifyAs("add")}
	large := &scriptModel{compose: composeAs("new-lesson", "always run the tests")}
	if rep := mustRun(t, input(small, large, t0)); rep.Status != RunOK || large.nCompose != 1 {
		t.Fatalf("report = %+v", rep)
	}
	p := pendingList(t, t0)[0]
	if it := p.Items[0]; it.Op != OpCreate || it.Name != "new-lesson" || len(it.Sources) != 0 {
		t.Fatalf("item = %+v", it)
	}
	if _, err := Approve(p.ID, t0.Add(time.Hour)); err != nil || f.published[0].Op != "create" {
		t.Fatalf("approve: %v, %+v", err, f.published)
	}
}

// The no-revival check runs when the add is queued: a name that was forgotten is not written, the
// proposal falls back to advice, and nothing can be approved.
func TestAddOverAForgottenNameIsNotQueuedAsAWrite(t *testing.T) {
	f := env(t)
	f.wrong("branches", "trunk is main", 1)
	f.forgotten["project:"+testProject+"/old-lesson"] = true
	small := &scriptModel{classify: classifyAs("add")}
	large := &scriptModel{compose: composeAs("old-lesson", "revived")}
	rep := mustRun(t, input(small, large, t0))
	if rep.Rejected != 1 {
		t.Fatalf("report = %+v", rep)
	}
	p := pendingList(t, t0)[0]
	if p.Items[0].Op != OpNote {
		t.Fatalf("item = %+v", p.Items[0])
	}
	if _, err := Approve(p.ID, t0.Add(time.Hour)); err == nil || f.nPublished() != 0 {
		t.Fatalf("an advice-only proposal was approvable: %v", err)
	}
}

// A replacement whose name is a superseded memory's (its file still exists) is a name that exists.
func TestReplaceNameOfASupersededMemoryIsRefused(t *testing.T) {
	f := env(t)
	f.wrong("old-note", "all stale", 1)
	f.plain("shelved", "x", 3)
	f.excluded["project:"+testProject+"/shelved"] = true
	small := &scriptModel{classify: classifyAs("supersede", "old-note")}
	large := &scriptModel{classify: classifyAs("supersede", "old-note"), compose: composeAs("shelved", "y")}
	mustRun(t, input(small, large, t0))
	if it := pendingList(t, t0)[0].Items[0]; it.Op != OpNote {
		t.Fatalf("item = %+v", it)
	}
}

// A compose answer the engine rejects is advice, not a write.
func TestInvalidComposeFallsBackToAdvice(t *testing.T) {
	f := env(t)
	f.wrong("old-note", "all stale", 1)
	small := &scriptModel{classify: classifyAs("supersede", "old-note")}
	large := &scriptModel{classify: classifyAs("supersede", "old-note"), compose: func(string) (string, error) { return `{"name":"Bad Name"}`, nil }}
	rep := mustRun(t, input(small, large, t0))
	if rep.Dropped != 1 {
		t.Fatalf("report = %+v", rep)
	}
	it := pendingList(t, t0)[0].Items[0]
	if it.Op != OpNote || it.Advice != "supersede" || it.Name != "old-note" {
		t.Fatalf("item = %+v", it)
	}
}

// The CAS race: a memory the replacement supersedes changes between the proposal and the approval.
// Approval refuses before writing anything, whichever side moved.
func TestApproveReplaceRefusesWhenASourceMoved(t *testing.T) {
	f := env(t)
	f.wrong("rule-a", "use make build", 1)
	f.plain("rule-b", "use make build please", 4)
	small := &scriptModel{classify: classifyAs("merge", "rule-a", "rule-b")}
	large := &scriptModel{classify: classifyAs("merge", "rule-a", "rule-b"), compose: composeAs("build-rule", "use make build")}
	mustRun(t, input(small, large, t0))
	p := pendingList(t, t0)[0]
	f.plain("rule-b", "use make build please, and lint", 5) // the second side moved
	_, err := Approve(p.ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != ProbStale || f.nPublished() != 0 {
		t.Fatalf("err = %v, published %d", err, f.nPublished())
	}
	if got, _ := loadProposal(p.ID); got.Status != StatusRefused {
		t.Fatalf("status = %s", got.Status)
	}
	v, _ := View(p.ID, t0)
	if v.Views[0].Stale == "" {
		t.Error("the view does not say a side moved")
	}
}

// The store's own check, under its lock, is the last word: a side that moved after validation but
// before the write is a refusal, the proposal is refused, and no item is marked published.
func TestApproveReplaceCarriesTheStoresStaleRefusal(t *testing.T) {
	f := env(t)
	p := queuedSupersede(t, f)
	f.pubErr = &memoryx.ReviewRefusal{Kind: memoryx.ReviewStale, Msg: "a side moved"}
	_, err := Approve(p.ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != memoryx.ReviewStale {
		t.Fatalf("err = %v", err)
	}
	got, _ := loadProposal(p.ID)
	if got.Status != StatusRefused || got.Items[0].Commit != "" {
		t.Fatalf("proposal = %+v", got)
	}
}

// Two answers that supersede the same memory cannot both apply (the second would be stale the
// moment the first is approved): one becomes a replace, the other advice.
func TestTwoSupersedesOfOneMemoryQueueOneReplace(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "old-note", Description: "d", Type: "project", Revision: 1, Body: "all stale",
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "wrong", Note: "names a retired branch", Revision: 1}, {By: "feedback", Rule: "stale", Note: "says develop", Revision: 1}}})
	small := &scriptModel{classify: classifyAs("supersede", "old-note")}
	large := &scriptModel{classify: classifyAs("supersede", "old-note"), compose: composeAs("new-note", "fresh")}
	mustRun(t, input(small, large, t0))
	items := pendingList(t, t0)[0].Items
	ops := map[string]int{}
	for _, it := range items {
		ops[it.Op]++
	}
	if len(items) != 2 || ops[OpReplace] != 1 || ops[OpNote] != 1 {
		t.Fatalf("items = %+v", items)
	}
}
