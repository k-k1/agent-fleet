package memoryx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rawEntryT reads a memory file of the demo project as the store holds it, superseded or not.
func rawEntryT(t *testing.T, name string) (agentMemEntry, bool) {
	t.Helper()
	b, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), "projects", consolPID(t), name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return agentMemEntry{}, false
	}
	e, pok := agentMemParse(b)
	if !pok {
		t.Fatalf("%s does not parse", name)
	}
	return e, true
}

func seedT(t *testing.T, now time.Time, names ...string) {
	t.Helper()
	c := agentMemCallerT(t, "claude-main")
	for _, n := range names {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: n, Description: "d " + n, Type: "project", Body: "body of " + n}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceItem(name string, srcs ...ConsolSource) ConsolItem {
	return ConsolItem{Op: "replace", Scope: "project", Name: name, Description: "merged", Type: "project",
		Body: "merged body of " + name, Sources: srcs}
}

func replaceT(t *testing.T, id int, it ConsolItem, now time.Time) (ConsolPublished, error) {
	t.Helper()
	it.ProjectID = consolPID(t)
	return PublishConsolidation(testPID(id), it, now)
}

// A supersede is ONE commit that writes the replacement and marks the old memory superseded; the
// change list shows one revertible row, and one revert restores the old memory and removes the new.
func TestReplaceSupersedeIsOneRevertibleChange(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "old-rule")
	before := commitCount(t)
	res, err := replaceT(t, 1, replaceItem("new-rule", ConsolSource{"old-rule", 1}), now)
	if err != nil || res.Revision != 1 {
		t.Fatalf("publish = %+v, %v", res, err)
	}
	if n := commitDelta(t, before); n != 1 {
		t.Fatalf("a supersede wrote %d commits, want 1", n)
	}
	msg := headMessage(t, res.Commit)
	for _, want := range []string{"AF-Op: replace", "AF-Author-Kind: consolidator", "AF-Consolidation-Proposal: " + testPID(1), "AF-Memory-Also: af/projects/" + consolPID(t) + "/old-rule.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
	old, _ := rawEntryT(t, "old-rule")
	if old.SupersededBy == nil || old.SupersededBy.Memory != "new-rule" || old.InvalidAt == "" || old.Revision != 2 || old.AuthorKind != "consolidator" {
		t.Fatalf("old = %+v", old)
	}
	if e, ok := rawEntryT(t, "new-rule"); !ok || e.Revision != 1 || e.SupersededBy != nil || e.Body != "merged body of new-rule" {
		t.Fatalf("new = %+v %v", e, ok)
	}
	// Agents see the replacement and not the old one.
	if _, err := agentMemPatch(agentMemCallerT(t, "codex-wt"), agentMemPatchReq{Name: "old-rule", Revision: 2,
		Edits: []agentMemPatchEdit{{Old: "body", New: ptr("x")}}}, now); err == nil {
		t.Fatal("an agent patched a superseded memory")
	}
	ch := agentMemChangesT(t)
	if ch[0].Commit != res.Commit || ch[0].Op != "replace" || ch[0].Name != "new-rule" || len(ch[0].Also) != 1 || ch[0].Also[0] != "old-rule" || !ch[0].Latest || !ch[0].Revertible {
		t.Fatalf("change list head = %+v", ch[0])
	}
	// The old memory's earlier row is no longer its newest change: only the unit is revertible.
	for _, c := range ch[1:] {
		if c.Name == "old-rule" && c.Latest {
			t.Fatalf("old-rule's create is still offered as the newest change: %+v", c)
		}
	}
	d, err := agentMemChangeDiff(res.Commit)
	if err != nil || d.Withheld || !strings.Contains(d.Diff, "old-rule.md") || !strings.Contains(d.Diff, "new-rule.md") {
		t.Fatalf("diff = %+v, %v", d, err)
	}

	before = commitCount(t)
	rv, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit, ExpectRevision: 1}, now)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if n := commitDelta(t, before); n != 1 {
		t.Fatalf("the revert wrote %d commits, want 1", n)
	}
	old, _ = rawEntryT(t, "old-rule")
	if old.SupersededBy != nil || old.InvalidAt != "" || old.Body != "body of old-rule" || old.Revision != 3 {
		t.Fatalf("old after revert = %+v", old)
	}
	if _, ok := rawEntryT(t, "new-rule"); ok {
		t.Fatal("the replacement survived the revert")
	}
	if !rv.Deleted {
		t.Fatalf("revert result = %+v", rv)
	}
	// The revert is itself one unit that can be undone.
	ch = agentMemChangesT(t)
	if ch[0].Commit != rv.Commit || ch[0].Op != "revert" || !ch[0].Revertible || !ch[0].Latest {
		t.Fatalf("revert row = %+v", ch[0])
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: rv.Commit}, now); err != nil {
		t.Fatalf("revert of the revert: %v", err)
	}
	if old, _ = rawEntryT(t, "old-rule"); old.SupersededBy == nil {
		t.Fatal("undoing the revert did not supersede again")
	}
	if _, ok := rawEntryT(t, "new-rule"); !ok {
		t.Fatal("undoing the revert did not bring the replacement back")
	}
}

// A merge of three memories reverts as a unit: all three come back, the umbrella goes.
func TestReplaceMergeOfThreeRevertsAllSides(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a", "b", "c")
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "b", Description: "d b", Type: "project", Body: "b two", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	res, err := replaceT(t, 2, replaceItem("umbrella", ConsolSource{"a", 1}, ConsolSource{"b", 2}, ConsolSource{"c", 1}), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if e, _ := rawEntryT(t, n); e.SupersededBy == nil || e.SupersededBy.Memory != "umbrella" {
			t.Fatalf("%s = %+v", n, e)
		}
	}
	ch := agentMemChangesT(t)
	if len(ch[0].Also) != 3 || !ch[0].Revertible {
		t.Fatalf("row = %+v", ch[0])
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit}, now); err != nil {
		t.Fatalf("revert: %v", err)
	}
	for n, body := range map[string]string{"a": "body of a", "b": "b two", "c": "body of c"} {
		e, ok := rawEntryT(t, n)
		if !ok || e.SupersededBy != nil || e.Body != body {
			t.Fatalf("%s after revert = %+v", n, e)
		}
	}
	if b, _ := rawEntryT(t, "b"); b.Revision != 4 {
		t.Fatalf("b's revision after the revert = %d, want 4 (never reused)", b.Revision)
	}
	if _, ok := rawEntryT(t, "umbrella"); ok {
		t.Fatal("the umbrella survived the revert")
	}
}

// Approval re-validates every side: whichever old memory moved between the proposal and the
// approval, nothing is written, not even the sides that still match.
func TestReplaceRefusesWhenAnySideMoved(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a", "b")
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "b", Description: "d", Type: "project", Body: "changed", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	n := commitCount(t)
	if _, err := replaceT(t, 3, replaceItem("both", ConsolSource{"a", 1}, ConsolSource{"b", 1}), now); reviewReason(err) != ReviewStale {
		t.Fatalf("moved second source: err = %v, want stale", err)
	}
	if _, err := replaceT(t, 3, replaceItem("both", ConsolSource{"a", 1}, ConsolSource{"gone", 1}), now); reviewReason(err) != ReviewStale {
		t.Fatalf("missing source: err = %v, want stale", err)
	}
	if commitCount(t) != n {
		t.Fatal("a refused replacement wrote a commit")
	}
	if e, _ := rawEntryT(t, "a"); e.SupersededBy != nil || e.Revision != 1 {
		t.Fatalf("a was touched: %+v", e)
	}
	if _, ok := rawEntryT(t, "both"); ok {
		t.Fatal("the replacement was written")
	}
}

// A memory that is already superseded cannot be replaced again, and its name cannot be taken by an add.
func TestReplaceNeverRevivesOrDoubleSupersedes(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a", "gone")
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "gone", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceT(t, 4, replaceItem("fresh", ConsolSource{"a", 1}), now); err != nil {
		t.Fatal(err)
	}
	n := commitCount(t)
	// The new name was forgotten.
	seedT(t, now, "other")
	n = commitCount(t)
	if _, err := replaceT(t, 5, replaceItem("gone", ConsolSource{"other", 1}), now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("forgotten name: err = %v", err)
	}
	// The new name is a superseded memory (its file still exists).
	if _, err := replaceT(t, 6, replaceItem("a", ConsolSource{"other", 1}), now); err == nil {
		t.Fatal("a replacement took the name of a superseded memory")
	}
	// An add over either is refused the same way.
	add := ConsolItem{Op: "create", Scope: "project", ProjectID: consolPID(t), Name: "a", Description: "d", Type: "project", Body: "x"}
	if _, err := PublishConsolidation(testPID(7), add, now); err == nil {
		t.Fatal("an add took the name of a superseded memory")
	}
	add.Name = "gone"
	if _, err := PublishConsolidation(testPID(8), add, now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("add over a forgotten name: err = %v", err)
	}
	// A superseded memory is not a source.
	if _, err := replaceT(t, 9, replaceItem("again", ConsolSource{"a", 2}), now); err == nil {
		t.Fatal("a superseded memory was superseded again")
	}
	if commitCount(t) != n {
		t.Fatal("a refused write committed")
	}
}

// If the commit step fails after the files were written, every side is put back: the replacement
// is gone, the old memories are untouched, and no commit exists.
func TestReplaceLeavesNothingHalfApplied(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a", "b")
	n := commitCount(t)
	snap := map[string]string{}
	for _, name := range []string{"a", "b"} {
		b, _, _ := agentMemReadFile(filepath.Join(agentMemDir(), "projects", consolPID(t), name+".md"))
		snap[name] = string(b)
	}
	// A stale index lock makes git refuse to stage: the failure comes after the files are written.
	lock := filepath.Join(memoryRepoDir(), "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := replaceT(t, 1, replaceItem("both", ConsolSource{"a", 1}, ConsolSource{"b", 1}), now)
	os.Remove(lock)
	if err == nil {
		t.Fatal("the write succeeded with git locked")
	}
	if commitCount(t) != n {
		t.Fatal("a failed replacement left a commit")
	}
	if _, ok := rawEntryT(t, "both"); ok {
		t.Fatal("the replacement file was left behind")
	}
	for name, want := range snap {
		b, _, _ := agentMemReadFile(filepath.Join(agentMemDir(), "projects", consolPID(t), name+".md"))
		if string(b) != want {
			t.Fatalf("%s changed on a failed replacement:\n%s", name, b)
		}
	}
	// The same proposal then goes through (a retry finds no half state).
	if _, err := replaceT(t, 1, replaceItem("both", ConsolSource{"a", 1}, ConsolSource{"b", 1}), now); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// Once any side moved after the replacement, the unit is no longer the newest change of that
// memory and the revert is refused (it would drop what came after).
func TestReplaceRevertRefusedAfterASideMoved(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	seedT(t, now, "a", "b")
	res, err := replaceT(t, 1, replaceItem("both", ConsolSource{"a", 1}, ConsolSource{"b", 1}), now)
	if err != nil {
		t.Fatal(err)
	}
	// The member edits the superseded memory afterwards.
	if _, err := agentMemSave(memberCallerT(t), agentMemSaveReq{Scope: "project", Name: "b", Description: "d", Type: "project", Body: "edited", Revision: 2}, now); err != nil {
		t.Fatal(err)
	}
	if ch := agentMemChangesT(t); ch[1].Commit == res.Commit && ch[1].Latest {
		t.Fatal("the unit is still offered as the newest change")
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit}, now); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("revert err = %v, want conflict", err)
	}
	// Forgetting one side of a unit is not offered either.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit, Forget: true}, now); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("forget err = %v, want conflict", err)
	}
}

func commitDelta(t *testing.T, before string) int {
	t.Helper()
	a, err1 := strconv.Atoi(before)
	b, err2 := strconv.Atoi(commitCount(t))
	if err1 != nil || err2 != nil {
		t.Fatalf("commit counts %q, %v / %v", before, err1, err2)
	}
	return b - a
}

func ptr(s string) *string { return &s }

func memberCallerT(t *testing.T) agentMemCaller {
	t.Helper()
	c, err := agentMemMemberCaller("project", consolPID(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
