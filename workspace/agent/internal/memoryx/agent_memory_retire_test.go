package memoryx

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func retireItem(name string, rev int, reason string) ConsolItem {
	return ConsolItem{Op: "retire", Scope: "project", Name: name, Revision: rev, Reason: reason}
}

func retireT(t *testing.T, id int, it ConsolItem, now time.Time) (ConsolPublished, error) {
	t.Helper()
	it.ProjectID = consolPID(t)
	return PublishConsolidation(testPID(id), it, now)
}

// A retire is one commit that archives the memory without deleting its file: agents stop seeing it
// (the stub says why), the change list offers one revert, and the revert brings back the text, the
// status and the reasons as they were.
func TestRetireArchivesInOneRevertibleChange(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "flake-note")
	prior, _ := rawEntryT(t, "flake-note")
	before := commitCount(t)
	res, err := retireT(t, 1, retireItem("flake-note", 1, "issue #2310 is closed"), now)
	if err != nil || res.Revision != 2 {
		t.Fatalf("publish = %+v, %v", res, err)
	}
	if n := commitDelta(t, before); n != 1 {
		t.Fatalf("a retire wrote %d commits, want 1", n)
	}
	msg := headMessage(t, res.Commit)
	for _, want := range []string{"AF-Op: retire", "AF-Author-Kind: consolidator", "AF-Consolidation-Proposal: " + testPID(1)} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
	e, ok := rawEntryT(t, "flake-note")
	if !ok || e.Status != "archived" || e.StatusBy != "consolidator" || e.InvalidAt == "" || e.Body != prior.Body {
		t.Fatalf("after retire = %+v (file kept: %v)", e, ok)
	}
	if len(e.Reasons) != 1 || e.Reasons[0].By != "consolidator" || e.Reasons[0].Rule != "retired" || e.Reasons[0].Note != "issue #2310 is closed" {
		t.Fatalf("reasons = %+v", e.Reasons)
	}
	// An agent no longer finds it; memory_read answers a stub that carries the reason, not the text.
	got, err := agentMemRead(agentMemCallerT(t, "codex-wt"), "project", "flake-note")
	if err != nil || !got.Stub || got.Body != "" || len(got.Reasons) != 1 {
		t.Fatalf("agent read = %+v, %v", got, err)
	}
	if ms, _ := ConsolMemories("project", consolPID(t)); len(ms) != 0 {
		t.Fatalf("the consolidator still reads %d memories", len(ms))
	}
	ch := agentMemChangesT(t)
	if ch[0].Commit != res.Commit || ch[0].Op != "retire" || !ch[0].Latest || !ch[0].Revertible {
		t.Fatalf("change list head = %+v", ch[0])
	}

	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit, ExpectRevision: 2}, now); err != nil {
		t.Fatalf("revert: %v", err)
	}
	back, ok := rawEntryT(t, "flake-note")
	if !ok || back.Status != "" || back.StatusBy != "" || back.InvalidAt != "" || back.Body != prior.Body || back.Description != prior.Description ||
		!reflect.DeepEqual(back.Reasons, prior.Reasons) || back.Revision != 3 {
		t.Fatalf("after revert = %+v, want the text of %+v", back, prior)
	}
	if ms, _ := ConsolMemories("project", consolPID(t)); len(ms) != 1 {
		t.Fatalf("the revert did not bring the memory back to the consolidator: %d", len(ms))
	}
}

// Everything the approval screen showed is checked again under the store lock, and a refusal writes
// nothing.
func TestRetireRefusals(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "moved", "pinned", "gone")
	if _, err := agentMemSave(memberCallerT(t), agentMemSaveReq{Scope: "project", Name: "moved", Description: "d", Type: "project", Body: "edited", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: consolPID(t), Name: "pinned", Pinned: true}, now); err != nil {
		t.Fatal(err)
	}
	pin, _ := rawEntryT(t, "pinned")
	if !pin.Pinned {
		t.Fatal("the fixture memory is not pinned")
	}
	cases := []struct {
		name string
		it   ConsolItem
		kind string
	}{
		{"moved since the proposal", retireItem("moved", 1, "done"), ReviewStale},
		{"pinned", retireItem("pinned", pin.Revision, "done"), ReviewStale},
		{"never existed", retireItem("nope", 1, "done"), ReviewStale},
		{"no reason", retireItem("gone", 1, " "), ReviewInvalid},
		{"two lines", retireItem("gone", 1, "a\nb"), ReviewInvalid},
		{"no revision", retireItem("gone", 0, "done"), ReviewInvalid},
	}
	n := commitCount(t)
	for i, c := range cases {
		_, err := retireT(t, 2+i, c.it, now)
		rf, ok := err.(*ReviewRefusal)
		if !ok || rf.Kind != c.kind {
			t.Errorf("%s: err = %v, want a %s refusal", c.name, err, c.kind)
		}
	}
	if commitCount(t) != n {
		t.Fatal("a refused retire still wrote a commit")
	}
	if _, err := retireT(t, 8, retireItem("gone", 1, "ok"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := retireT(t, 9, retireItem("gone", 2, "again"), now); err == nil {
		t.Fatal("a memory that is archived already was retired again")
	}
}

// A retry of a published retire writes nothing (the proposal trailer is found), like any item.
func TestRetireRetryWritesNothing(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a")
	first, err := retireT(t, 1, retireItem("a", 1, "done"), now)
	if err != nil {
		t.Fatal(err)
	}
	n := commitCount(t)
	again, err := retireT(t, 1, retireItem("a", 1, "done"), now)
	if err != nil || !again.Already || again.Commit != first.Commit || commitCount(t) != n {
		t.Fatalf("retry = %+v, %v", again, err)
	}
}
