package memoryx

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func agentMemChangesT(t *testing.T) []agentMemChangeView {
	t.Helper()
	ch, err := agentMemListChanges(0)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// The list shows who changed what, newest first, and marks only the newest change of each
// memory as the one that can be reverted.
func TestAgentMemoryChangesList(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	claudeC, codexC := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	if _, err := agentMemSave(claudeC, agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(codexC, agentMemSaveReq{Name: "a", Description: "d", Body: "v2", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(claudeC, agentMemSaveReq{Scope: "user", Name: "u", Description: "d", Body: "x"}, now); err != nil {
		t.Fatal(err)
	}
	// A plain snapshot is not an agent memory change and stays out of the list.
	if _, err := memorySnapshot(memoryTriggerManual, now); err != nil {
		t.Fatal(err)
	}
	ch := agentMemChangesT(t)
	if len(ch) != 3 {
		t.Fatalf("changes = %+v", ch)
	}
	if ch[0].Name != "u" || ch[0].Scope != "user" || ch[0].Op != "create" || !ch[0].Latest || ch[0].Project != nil {
		t.Errorf("newest = %+v", ch[0])
	}
	if ch[1].Name != "a" || ch[1].Op != "update" || ch[1].AuthorKind != "codex" || ch[1].AuthorSession != "codex-wt" || !ch[1].Latest || !ch[1].Live {
		t.Errorf("update = %+v", ch[1])
	}
	if ch[1].Project == nil || ch[1].Project.Display != "demo" {
		t.Errorf("project = %+v", ch[1].Project)
	}
	if ch[2].Latest || ch[2].AuthorKind != "claude" {
		t.Errorf("older change = %+v", ch[2])
	}
}

// Reverting an update writes the earlier text back as a new revision by the member; reverting
// that revert restores the later text; an older change cannot be reverted past a newer one.
func TestAgentMemoryRevertUpdate(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "v2", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	ch := agentMemChangesT(t)
	older, newer := ch[1].Commit, ch[0].Commit
	if _, err := agentMemRevert(agentMemRevertReq{Commit: older}, now); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("reverting past a newer change: %v", err)
	}
	res, err := agentMemRevert(agentMemRevertReq{Commit: newer}, now)
	if err != nil || res.Revision != 3 {
		t.Fatalf("revert = %+v, %v", res, err)
	}
	e, _ := agentMemRead(c, "", "a")
	if e.Body != "v1" || e.Revision != 3 || e.AuthorKind != "member" || e.AuthorSession != "console" {
		t.Fatalf("after revert = %+v", e)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if !strings.Contains(msg, "AF-Op: revert") || !strings.Contains(msg, "AF-Revert-Of: "+newer) || !strings.Contains(msg, "AF-Author-Kind: member") {
		t.Fatalf("revert commit:\n%s", msg)
	}
	// A reader holding revision 2 is refused: the revert moved the revision on.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "stale", Revision: 2}, now); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("stale save after revert: %v", err)
	}
	ch = agentMemChangesT(t)
	if ch[0].Op != "revert" || ch[0].RevertOf != newer {
		t.Fatalf("list after revert = %+v", ch[0])
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: ch[0].Commit}, now); err != nil {
		t.Fatal(err)
	}
	if e, _ := agentMemRead(c, "", "a"); e.Body != "v2" || e.Revision != 4 {
		t.Fatalf("after reverting the revert = %+v", e)
	}
}

// Reverting a create removes the memory; reverting a forget brings it back; forget from the
// Console removes it as the change left it.
func TestAgentMemoryRevertCreateAndForget(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	created := agentMemChangesT(t)[0].Commit
	if res, err := agentMemRevert(agentMemRevertReq{Commit: created}, now); err != nil || !res.Deleted {
		t.Fatalf("revert create = %+v, %v", res, err)
	}
	if _, err := agentMemRead(c, "", "a"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Fatalf("read after reverting the create: %v", err)
	}
	removal := agentMemChangesT(t)[0]
	if removal.Live || !removal.Latest {
		t.Fatalf("removal row = %+v", removal)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: removal.Commit}, now); err != nil {
		t.Fatal(err)
	}
	e, err := agentMemRead(c, "", "a")
	if err != nil || e.Body != "v1" || e.Revision != 2 {
		t.Fatalf("after reverting the removal = %+v, %v", e, err)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: agentMemChangesT(t)[0].Commit, Forget: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemRead(c, "", "a"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Fatalf("read after a Console forget: %v", err)
	}
	if msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch); !strings.Contains(msg, "AF-Op: forget") || !strings.Contains(msg, "AF-Author-Kind: member") {
		t.Fatalf("forget commit:\n%s", msg)
	}
}

// A revert that would bring back a secret is refused unless the member acknowledges it.
func TestAgentMemoryRevertScansUnlessAcknowledged(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "v2", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	// The earlier text was clean when it was written; a rule added since flags it, which is
	// exactly when a restore has to scan again (ADR 0108 decision 9).
	ch := agentMemChangesT(t)
	saved := memorySecretRules
	t.Cleanup(func() { memorySecretRules = saved })
	memorySecretRules = append(append([]memorySecretRule{}, saved...), memorySecretRule{Name: "test-v1", Re: regexpMustV1()})
	_, err := agentMemRevert(agentMemRevertReq{Commit: ch[0].Commit}, now)
	var se *agentMemSecretErr
	if !errors.As(err, &se) {
		t.Fatalf("revert of a flagged body = %v", err)
	}
	for _, f := range se.Findings {
		if f.Hint == "v1" {
			t.Fatalf("finding carries the value: %+v", f)
		}
	}
	if res, err := agentMemRevert(agentMemRevertReq{Commit: ch[0].Commit, Ack: true}, now); err != nil || res.Revision != 3 {
		t.Fatalf("acknowledged revert = %+v, %v", res, err)
	}
}

func TestAgentMemoryRevertRejectsForeignCommits(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	snap, err := memorySnapshot(memoryTriggerManual, now)
	if err != nil || !snap.Committed {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	for _, commit := range []string{snap.Rev, "nothex!", "deadbeefdeadbeef", "-n1"} {
		if _, err := agentMemRevert(agentMemRevertReq{Commit: commit}, now); agentMemCode(err) != errCodeMemoryBadRev {
			t.Errorf("%q: %v", commit, err)
		}
	}
}

func TestAgentMemoryChangesHandlers(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	mux := buildMux()
	if _, err := agentMemSave(agentMemCallerT(t, "claude-main"), agentMemSaveReq{Name: "a", Description: "d", Body: "v1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/changes", "", "")
	var got agentMemChangesWire
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Changes) != 1 {
		t.Fatalf("changes %d: %s", w.Code, w.Body)
	}
	w = smokeDo(t, mux, http.MethodPost, "/agents/memory/entries/revert", "", `{"commit":"`+got.Changes[0].Commit+`"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":true`) {
		t.Fatalf("revert %d: %s", w.Code, w.Body)
	}
}

// regexpMustV1 flags the line "v1", standing in for a secret rule the earlier text would trip.
func regexpMustV1() *regexp.Regexp { return regexp.MustCompile(`^v1$`) }
