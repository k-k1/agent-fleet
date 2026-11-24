package memoryx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

// promoteSaved is ten days back: past the default quiet period of seven, inside one of eleven.
var promoteSaved = time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)

func promoteSave(t *testing.T, c agentMemCaller, name, kind, body string, anchors ...string) {
	t.Helper()
	req := agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, MemKind: &kind}
	if anchors != nil {
		req.Anchors = &anchors
	}
	if _, err := agentMemSave(c, req, promoteSaved); err != nil {
		t.Fatal(err)
	}
}

func promoteConfirm(t *testing.T, name string, rev int, sessions ...agentMemCaller) {
	t.Helper()
	for _, c := range sessions {
		if _, err := agentMemFeedback(c, agentMemFeedbackReq{Scope: agentMemScopeProject, Name: name, Revision: rev, Verdict: agentMemVerdictConfirmed}, promoteSaved); err != nil {
			t.Fatalf("confirm %s by %s: %v", name, c.Session, err)
		}
	}
}

func promoteGet(t *testing.T, q string) agentMemPromoteWire {
	t.Helper()
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/promotion-candidates?"+q, nil)
	var w agentMemPromoteWire
	if code != http.StatusOK || json.Unmarshal(b, &w) != nil {
		t.Fatalf("promotion-candidates %s: %d %s", q, code, b)
	}
	return w
}

func promoteHeldRules(w agentMemPromoteWire, name string) []string {
	for _, h := range w.Held {
		if h.Name == name {
			var out []string
			for _, m := range h.Missing {
				out = append(out, m.Rule+"="+m.Detail)
			}
			return out
		}
	}
	return nil
}

// The whole route over a real repository read by ref: one memory qualifies, each of the others
// misses one signal and says which, the kinds that never promote and an archived memory are not
// listed, and a run writes nothing to the store or the working copy.
func TestAgentMemoryPromoteCandidatesFromSignals(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	agentMemLintOrigin(t, clone, map[string]string{
		"scripts/release.sh": "#!/bin/sh\n",
		"docs/build.md":      "# build\n",
	})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	if a.Session == b.Session {
		t.Fatal("the fixture needs two sessions")
	}
	promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b, a) // a twice is still one session

	promoteSave(t, a, "gone-anchor", "lesson", "see scripts/gone.sh", "scripts/gone.sh")
	promoteConfirm(t, "gone-anchor", 1, a, b)

	promoteSave(t, a, "one-session", "preference", "docs/build.md first", "docs/build.md")
	promoteConfirm(t, "one-session", 1, a)

	// Confirmed on revision 1, then changed: the confirmations are for the earlier body.
	promoteSave(t, a, "changed", "lesson", "docs/build.md v1", "docs/build.md")
	promoteConfirm(t, "changed", 1, a, b)
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "changed", Description: "d", Type: "project", Body: "docs/build.md v2", Revision: 1}, promoteSaved); err != nil {
		t.Fatal(err)
	}

	promoteSave(t, a, "no-anchor", "lesson", "plain advice")
	promoteConfirm(t, "no-anchor", 1, a, b)

	promoteSave(t, a, "a-state", "state", "docs/build.md", "docs/build.md")
	promoteConfirm(t, "a-state", 1, a, b)
	promoteSave(t, a, "archived", "lesson", "docs/build.md", "docs/build.md")
	promoteConfirm(t, "archived", 1, a, b)
	lifeSet(t, a, agentMemScopeProject, "archived", func(e *agentMemEntry) { e.Status = agentMemStatusArchived })

	AgentMemoryEnabled = func() bool { return false } // the list is the member's, not gated by the sessions' switch
	storeBefore := agentMemLintSnapshot(t, agentMemDir())
	cloneBefore := agentMemLintSnapshot(t, clone)
	w := promoteGet(t, "project="+a.Project.ID)
	if !reflectEq(storeBefore, agentMemLintSnapshot(t, agentMemDir())) {
		t.Error("a run changed the memory store (a file or a sidecar)")
	}
	if !reflectEq(cloneBefore, agentMemLintSnapshot(t, clone)) {
		t.Error("a run changed the working copy")
	}

	if w.Ref != "origin/main" || w.Commit == "" || w.MinSessions != 2 || w.QuietDays != 7 || len(w.Skipped) != 0 {
		t.Fatalf("header: %+v", w)
	}
	if len(w.Candidates) != 1 || w.Candidates[0].Name != "release-steps" || w.Candidates[0].Sessions != 2 || w.Candidates[0].Revision != 1 {
		t.Fatalf("candidates = %+v", w.Candidates)
	}
	want := map[string]string{
		"gone-anchor": "lint=missing-path",
		"one-session": "confirmations=1 of 2 sessions on revision 1",
		"changed":     "confirmations=0 of 2 sessions on revision 2",
		"no-anchor":   "no-anchors=",
	}
	if len(w.Held) != len(want) {
		t.Errorf("held = %+v", w.Held)
	}
	for name, rule := range want {
		if got := promoteHeldRules(w, name); strings.Join(got, ";") != rule {
			t.Errorf("%s: missing %v, want %s", name, got, rule)
		}
	}

	// The quiet period: everything above was written ten days ago, so eleven days hold it.
	w = promoteGet(t, "project="+a.Project.ID+"&days=11")
	if len(w.Candidates) != 0 || !strings.HasPrefix(strings.Join(promoteHeldRules(w, "release-steps"), ";"), "changed-recently=") {
		t.Errorf("days=11: candidates %+v, held %v", w.Candidates, promoteHeldRules(w, "release-steps"))
	}
}

// Without a readable default branch no anchor is known to exist, so nothing is a candidate and the
// answer says why.
func TestAgentMemoryPromoteCandidatesNeedTheRepository(t *testing.T) {
	agentMemTestEnv(t)
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	w := promoteGet(t, "project="+a.Project.ID) // the fixture has no origin default branch
	if len(w.Candidates) != 0 || len(w.Skipped) != 1 || !strings.Contains(w.Skipped[0], "repository checks") {
		t.Fatalf("candidates %+v, skipped %v", w.Candidates, w.Skipped)
	}
	if got := promoteHeldRules(w, "release-steps"); strings.Join(got, ";") != memorypromote.RuleUnverified+"=" {
		t.Errorf("held: %v", got)
	}
}

func TestAgentMemoryPromoteCandidatesRefusesBadInput(t *testing.T) {
	agentMemTestEnv(t)
	a := agentMemCallerT(t, "claude-main")
	promoteSave(t, a, "m", "lesson", "x")
	for _, q := range []string{"", "project=nope", "project=" + a.Project.ID + "&days=0", "project=" + a.Project.ID + "&days=91", "project=" + a.Project.ID + "&days=x"} {
		if code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/promotion-candidates?"+q, nil); code < 400 || code >= 500 {
			t.Errorf("%q: %d %s", q, code, b)
		}
	}
}
