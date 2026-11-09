package memoryx

import (
	"strings"
	"testing"
	"time"
)

func consolPID(t *testing.T) string { return agentMemCallerT(t, "claude-main").Project.ID }

// An approved patch takes the tools' own path: one revision, one commit, authored as the
// consolidator with the proposal id as a trailer, visible in the change list and revertible.
func TestPublishConsolidationPatchIsAnAuthoredRevertibleChange(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "build-cmd", Description: "d", Type: "project", Body: "run make old"}, now); err != nil {
		t.Fatal(err)
	}
	it := ConsolItem{Op: "patch", Scope: "project", ProjectID: consolPID(t), Name: "build-cmd", Revision: 1,
		Edits: []ConsolEdit{{Old: "make old", New: "make new"}}}
	res, err := PublishConsolidation(testPID(1), it, now)
	if err != nil || res.Already || res.Revision != 2 {
		t.Fatalf("publish = %+v, %v", res, err)
	}
	msg := headMessage(t, res.Commit)
	for _, want := range []string{"AF-Op: update", "AF-Author-Kind: consolidator", "AF-Consolidation-Proposal: " + testPID(1)} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
	if e, err := agentMemRead(agentMemCallerT(t, "codex-wt"), "project", "build-cmd"); err != nil || e.Body != "run make new" || e.AuthorKind != "consolidator" {
		t.Fatalf("entry = %+v, %v", e, err)
	}
	ch := agentMemChangesT(t)
	if len(ch) == 0 || ch[0].AuthorKind != "consolidator" || !ch[0].Latest {
		t.Fatalf("change list = %+v", ch)
	}
	// A retry of the same proposal writes nothing.
	before := commitCount(t)
	again, err := PublishConsolidation(testPID(1), it, now)
	if err != nil || !again.Already || again.Commit != res.Commit || commitCount(t) != before {
		t.Fatalf("retry = %+v, %v (commits %s -> %s)", again, err, before, commitCount(t))
	}
	// The way back is the ordinary revert.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit}, now); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if e, _ := agentMemRead(agentMemCallerT(t, "codex-wt"), "project", "build-cmd"); e.Body != "run make old" {
		t.Fatalf("after revert body = %q", e.Body)
	}
}

// A patch made against an older revision, or whose text is gone, is refused as stale and writes
// nothing: approval re-validates against the memory as it is now.
func TestPublishConsolidationRefusesAStalePatch(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "one two"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "one three", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	before := commitCount(t)
	it := ConsolItem{Op: "patch", Scope: "project", ProjectID: consolPID(t), Name: "a", Revision: 1,
		Edits: []ConsolEdit{{Old: "two", New: "2"}}}
	if _, err := PublishConsolidation(testPID(2), it, now); reviewReason(err) != ReviewStale {
		t.Fatalf("old revision: err = %v", err)
	}
	it.Revision = 2 // current revision, but the text the edit anchors on is gone
	if _, err := PublishConsolidation(testPID(2), it, now); reviewReason(err) == "" {
		t.Fatalf("missing old text: err = %v, want a refusal", err)
	}
	it.Name = "nope"
	if _, err := PublishConsolidation(testPID(2), it, now); reviewReason(err) != ReviewStale {
		t.Fatalf("missing memory: err = %v", err)
	}
	if commitCount(t) != before {
		t.Fatal("a refused proposal wrote a commit")
	}
}

// ADR 0130 decision 6: a create over a forgotten name is refused, with the tombstone present and
// with it lost. A mutation that drops noRevival turns this red.
func TestPublishConsolidationNeverRevives(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "gone", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "gone", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	it := ConsolItem{Op: "create", Scope: "project", ProjectID: consolPID(t), Name: "gone", Description: "d", Type: "project", Body: "revived"}
	before := commitCount(t)
	if _, err := PublishConsolidation(testPID(3), it, now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("err = %v, want forgotten", err)
	}
	if commitCount(t) != before {
		t.Fatal("a refused create wrote a commit")
	}
	it.Name = "fresh"
	if res, err := PublishConsolidation(testPID(4), it, now); err != nil || res.Revision != 1 {
		t.Fatalf("fresh create = %+v, %v", res, err)
	}
}

func TestConsolMemoriesLeavesOutWhatAgentsCannotSee(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	for _, n := range []string{"b", "a"} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: n, Description: "d", Type: "project", Body: "x"}, now); err != nil {
			t.Fatal(err)
		}
	}
	ms, err := ConsolMemories("project", consolPID(t))
	if err != nil || len(ms) != 2 || ms[0].Name != "a" || ms[1].Name != "b" {
		t.Fatalf("memories = %+v, %v", ms, err)
	}
	if ms, err := ConsolMemories("project", "no-such-project"); err != nil || len(ms) != 0 {
		t.Fatalf("unknown project = %+v, %v", ms, err)
	}
	sc, err := ConsolScopes()
	if err != nil || len(sc) < 2 || sc[0].Scope != "user" || sc[1].ProjectID != consolPID(t) {
		t.Fatalf("scopes = %+v, %v", sc, err)
	}
}
