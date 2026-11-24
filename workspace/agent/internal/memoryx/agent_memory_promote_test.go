package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// promoteSaved is ten days back: past the default quiet period of seven, inside one of eleven.
var promoteSaved = time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)

// promoteBackdate makes the store's commits carry promoteSaved, as if they had been made then:
// the quiet period is read from the store's history, which takes the clock, not the save's now.
func promoteBackdate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_COMMITTER_DATE", promoteSaved.Format(time.RFC3339))
}

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

func promoteNames(w agentMemPromoteWire) []string {
	var out []string
	for _, c := range w.Candidates {
		out = append(out, c.Name)
	}
	return out
}

// The whole route over a real repository read by ref: one memory qualifies, each of the others
// misses one signal and says which, the kinds that never promote and an archived memory are not
// listed, and a run writes nothing to the store or the working copy.
func TestAgentMemoryPromoteCandidatesFromSignals(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{
		"scripts/release.sh": "#!/bin/sh\n",
		"docs/build.md":      "# build\n",
		"README.md":          "# demo\n",
		".gitignore":         "build/\n",
		"main.go":            "package main\nfunc releaseAll() {}\n",
	})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	if a.Session == b.Session {
		t.Fatal("the fixture needs two sessions")
	}
	promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh, see README.md", "scripts/release.sh", "README.md", "`releaseAll`")
	promoteConfirm(t, "release-steps", 1, a, b, a) // a twice is still one session

	promoteSave(t, a, "gone-anchor", "lesson", "see scripts/gone.sh", "scripts/gone.sh")
	promoteConfirm(t, "gone-anchor", 1, a, b)

	// Anchors lint lets pass unchecked: a root-level file, a command, an ignored path not in the tree.
	promoteSave(t, a, "root-file", "lesson", "read it first", "GONE.md")
	promoteConfirm(t, "root-file", 1, a, b)
	promoteSave(t, a, "a-command", "lesson", "run it", "make release")
	promoteConfirm(t, "a-command", 1, a, b)
	promoteSave(t, a, "ignored-dir", "lesson", "outputs", "build/")
	promoteConfirm(t, "ignored-dir", 1, a, b)

	promoteSave(t, a, "one-session", "preference", "docs/build.md first", "docs/build.md")
	promoteConfirm(t, "one-session", 1, a)

	// Confirmed on revision 1, then changed: the confirmations are for the earlier body.
	promoteSave(t, a, "changed", "lesson", "docs/build.md v1", "docs/build.md")
	promoteConfirm(t, "changed", 1, a, b)
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "changed", Description: "d", Type: "project", Body: "docs/build.md v2", Revision: 1}, promoteSaved); err != nil {
		t.Fatal(err)
	}

	// A link that never had a target: lint and the anchor check say so; nothing moved.
	promoteSave(t, a, "ghost-link", "lesson", "see the other note", "[[ghost]]")
	promoteConfirm(t, "ghost-link", 1, a, b)

	promoteSave(t, a, "no-anchor", "lesson", "plain advice")
	promoteConfirm(t, "no-anchor", 1, a, b)

	promoteSave(t, a, "a-state", "state", "docs/build.md", "docs/build.md")
	promoteConfirm(t, "a-state", 1, a, b)
	promoteSave(t, a, "archived", "lesson", "docs/build.md", "docs/build.md")
	promoteConfirm(t, "archived", 1, a, b)
	lifeSet(t, a, agentMemScopeProject, "archived", func(e *agentMemEntry) { e.Status = agentMemStatusArchived })

	AgentMemoryEnabled = func() bool { return false } // the list is the member's, not gated by the sessions' switch
	// The history is read from the bare repository: a missing staging directory stays missing.
	if err := os.RemoveAll(memoryStagingDir()); err != nil {
		t.Fatal(err)
	}
	storeBefore := agentMemLintSnapshot(t, agentMemDir())
	cloneBefore := agentMemLintSnapshot(t, clone)
	w := promoteGet(t, "project="+a.Project.ID)
	if !reflectEq(storeBefore, agentMemLintSnapshot(t, agentMemDir())) {
		t.Error("a run changed the memory store (a file or a sidecar)")
	}
	if !reflectEq(cloneBefore, agentMemLintSnapshot(t, clone)) {
		t.Error("a run changed the working copy")
	}
	if _, err := os.Stat(memoryStagingDir()); !os.IsNotExist(err) {
		t.Errorf("a run created the store's staging directory: %v", err)
	}

	if w.Ref != "origin/main" || w.Commit == "" || w.MinSessions != 2 || w.QuietDays != 7 || len(w.Skipped) != 0 {
		t.Fatalf("header: %+v", w)
	}
	if len(w.Candidates) != 1 || w.Candidates[0].Name != "release-steps" || w.Candidates[0].Sessions != 2 || w.Candidates[0].Revision != 1 {
		t.Fatalf("candidates = %+v\nheld = %+v", w.Candidates, w.Held)
	}
	want := map[string]string{
		"gone-anchor": "anchor-not-found=scripts/gone.sh;lint=missing-path",
		"root-file":   "anchor-not-found=GONE.md",
		"a-command":   "anchor-not-found=make release",
		"ignored-dir": "anchor-not-found=build/",
		"one-session": "confirmations=1 of 2 sessions on revision 1",
		"changed":     "confirmations=0 of 2 sessions on revision 2",
		"no-anchor":   "no-anchors=",
		"ghost-link":  "anchor-not-found=[[ghost]];lint=broken-link",
	}
	if len(w.Held) != len(want) {
		t.Errorf("held = %+v", w.Held)
	}
	for name, rule := range want {
		if got := promoteHeldRules(w, name); strings.Join(got, ";") != rule {
			t.Errorf("%s: missing %v, want %s", name, got, rule)
		}
	}

	// The quiet period: everything above was published ten days ago, so eleven days hold it.
	w = promoteGet(t, "project="+a.Project.ID+"&days=11")
	if len(w.Candidates) != 0 || !strings.HasPrefix(strings.Join(promoteHeldRules(w, "release-steps"), ";"), "changed-recently=") {
		t.Errorf("days=11: candidates %+v, held %v", w.Candidates, promoteHeldRules(w, "release-steps"))
	}
}

// A revision made today by a lint reason write keeps the old updated stamp; two sessions
// confirming it today must still wait the quiet period, which is read from the store's history.
func TestAgentMemoryPromoteQuietPeriodFollowsTheRevision(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n"})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "m", "procedure", "run scripts/release.sh", "scripts/release.sh")
	if err := os.Unsetenv("GIT_COMMITTER_DATE"); err != nil { // the reason write below happens today
		t.Fatal(err)
	}
	e := lifeStored(t, a, agentMemScopeProject, "m")
	wrote, err := agentMemLintWrite(agentMemLintCaller(a), agentMemScopeProject, e, time.Now(), func(cur *agentMemEntry) (bool, []string) {
		cur.Reasons = nil // a reason cleared: the revision moves, the body and its stamp do not
		return true, nil
	}, agentMemOpLint)
	if err != nil || !wrote {
		t.Fatalf("lint write: %v %v", wrote, err)
	}
	if got := lifeStored(t, a, agentMemScopeProject, "m"); got.Revision != 2 || got.Updated != e.Updated {
		t.Fatalf("fixture: revision %d updated %q (was %q)", got.Revision, got.Updated, e.Updated)
	}
	promoteConfirm(t, "m", 2, a, b)
	w := promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || !strings.HasPrefix(strings.Join(promoteHeldRules(w, "m"), ";"), "changed-recently=") {
		t.Fatalf("candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
	}
}

// A file that differs from its last published change is held: a broken revision line reads as
// revision 1, and the confirmations of the published revision 1 must not vouch for this text.
func TestAgentMemoryPromoteHoldsAnUnpublishedFile(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n"})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "m", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "m", 1, a, b)
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "m" {
		t.Fatalf("fixture: %+v %+v", w.Candidates, w.Held)
	}
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "m", Description: "d", Type: "project", Body: "run scripts/release.sh twice", Revision: 1}, promoteSaved); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(agentMemDir(), "projects", a.Project.ID, "m.md")
	raw, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(raw), "revision: 2\n") {
		t.Fatalf("fixture file: %v\n%s", err, raw)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), "revision: 2\n", "revision: two\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lifeStored(t, a, agentMemScopeProject, "m"); got.Revision != 1 {
		t.Fatalf("the broken line should read as revision 1, got %d", got.Revision)
	}
	w := promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || strings.Join(promoteHeldRules(w, "m"), ";") != memorypromote.RuleUnpublished+"=the file differs from its last published change" {
		t.Fatalf("candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
	}
	// A pin republishes the file as it parsed, revision 1 over revision 2's text: the file now is
	// its last published change, and the history shows the revision going back.
	if _, err := agentMemPin(agentMemPinReq{Scope: agentMemScopeProject, Project: a.Project.ID, Name: "m", Pinned: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || strings.Join(promoteHeldRules(w, "m"), ";") != memorypromote.RuleUnpublished+"=the revision does not follow the change before it" {
		t.Fatalf("after a pin: candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
	}
	// Unpinned again: the last two changes now agree with each other, and the walk back to where
	// revision 1 began still finds revision 2 before it.
	if _, err := agentMemPin(agentMemPinReq{Scope: agentMemScopeProject, Project: a.Project.ID, Name: "m", Pinned: false}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || strings.Join(promoteHeldRules(w, "m"), ";") != memorypromote.RuleUnpublished+"=the revision does not follow the change before it" {
		t.Fatalf("after pin and unpin: candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
	}
}

// A pin republishes a file under its revision number as it stands: an edit outside AF to the type
// or the anchors, which a save would have given a new revision, must not let the published
// revision's confirmations vouch for it.
func TestAgentMemoryPromoteHoldsMetadataPublishedByAPin(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n"})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	kind := "procedure"
	anchors := []string{"scripts/release.sh"}
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "about-me", Description: "d", Type: "user", Body: "run scripts/release.sh", MemKind: &kind, Anchors: &anchors}, promoteSaved); err != nil {
		t.Fatal(err)
	}
	promoteConfirm(t, "about-me", 1, a, b)
	promoteSave(t, a, "gone", "procedure", "run the release", "scripts/gone.sh")
	promoteConfirm(t, "gone", 1, a, b)
	for name, edit := range map[string][2]string{
		"about-me": {`type: "user"`, `type: "project"`},
		"gone":     {`"scripts/gone.sh"`, `"scripts/release.sh"`},
	} {
		p := filepath.Join(agentMemDir(), "projects", a.Project.ID, name+".md")
		raw, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(raw), edit[0]) {
			t.Fatalf("%s fixture: %v\n%s", name, err, raw)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(raw), edit[0], edit[1], 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := agentMemPin(agentMemPinReq{Scope: agentMemScopeProject, Project: a.Project.ID, Name: name, Pinned: true}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := lifeStored(t, a, agentMemScopeProject, name); got.Revision != 1 {
			t.Fatalf("%s: the pin moved the revision to %d", name, got.Revision)
		}
	}
	w := promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 {
		t.Fatalf("candidates %v", promoteNames(w))
	}
	for _, name := range []string{"about-me", "gone"} {
		if got := strings.Join(promoteHeldRules(w, name), ";"); got != memorypromote.RuleUnpublished+"=the revision does not follow the change before it" {
			t.Errorf("%s: held %v", name, got)
		}
	}
}

// A wrong report whose reason never reached the memory (the reason's commit failed after the
// record was written, and a retry is a duplicate) still holds the revision it was made on; a new
// revision starts clean.
func TestAgentMemoryPromoteHoldsADisputeWithoutItsReason(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n"})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "m", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "m", 1, a, b)
	rel, _ := agentMemScopeDir(agentMemScopeProject, a)
	if err := agentMemFeedbackAppend(rel, "m", agentMemFeedbackRecord{Session: "third", Revision: 1, Verdict: agentMemVerdictWrong, At: promoteSaved.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if got := lifeStored(t, a, agentMemScopeProject, "m"); len(got.Reasons) != 0 {
		t.Fatalf("fixture: reasons %+v", got.Reasons)
	}
	w := promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || strings.Join(promoteHeldRules(w, "m"), ";") != memorypromote.RuleDisputed+"=1 session(s) on revision 1" {
		t.Fatalf("candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
	}
	// The retry the reviewer described: the same session's report is a duplicate and changes nothing.
	if res, err := agentMemFeedback(agentMemCaller{Session: "third", Kind: "claude", Project: a.Project}, agentMemFeedbackReq{Scope: agentMemScopeProject, Name: "m", Revision: 1, Verdict: agentMemVerdictWrong, Note: "x"}, promoteSaved); err != nil || res.Outcome != "duplicate" {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if w := promoteGet(t, "project="+a.Project.ID); len(w.Candidates) != 0 {
		t.Fatalf("after the retry: candidates %v", promoteNames(w))
	}
	// A corrected text is revision 2; its own confirmations make it a candidate again.
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "m", Description: "d", Type: "project", Body: "run scripts/release.sh, then tag", Revision: 1}, promoteSaved); err != nil {
		t.Fatal(err)
	}
	promoteConfirm(t, "m", 2, a, b)
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "m" {
		t.Fatalf("revision 2: held %v", promoteHeldRules(w, "m"))
	}
}

// Every way the anchors cannot be checked holds the candidate as unverified and says why: no
// origin default branch, a default only guessed because origin/HEAD is not set, a retired-term
// list that cannot be read.
func TestAgentMemoryPromoteCandidatesNeedTheRepository(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	check := func(label, skipped string) {
		t.Helper()
		w := promoteGet(t, "project="+a.Project.ID)
		if len(w.Candidates) != 0 || !strings.Contains(strings.Join(w.Skipped, ";"), skipped) {
			t.Errorf("%s: candidates %v, skipped %v", label, promoteNames(w), w.Skipped)
		}
		if got := promoteHeldRules(w, "release-steps"); len(got) != 1 || !strings.HasPrefix(got[0], memorypromote.RuleUnverified+"=") {
			t.Errorf("%s: held %v", label, got)
		}
	}
	check("no origin", "repository checks")

	// origin/main holds the anchor, but the default is develop, which lacks it; origin/HEAD unset.
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n"})
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "release-steps" {
		t.Fatalf("with origin/HEAD the memory qualifies: %+v %v", w.Held, w.Skipped)
	}
	for _, args := range [][]string{
		{"rm", "--quiet", "scripts/release.sh"},
		{"commit", "--quiet", "-m", "drop"},
		{"update-ref", "refs/remotes/origin/develop", "HEAD"},
		{"symbolic-ref", "--delete", "refs/remotes/origin/HEAD"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatal(err)
		}
	}
	check("guessed default", "origin/HEAD")

	if err := agentMemGit(clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{not json", "null", `{"terms":null}`, `{"rejected":[]}`, `[]`} {
		if err := os.WriteFile(agentMemLintTermsPath(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		check("term list "+bad, "retired-term")
	}
	// A list that cannot be read at all (here a directory in its place).
	if err := os.Remove(agentMemLintTermsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(agentMemLintTermsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	check("unreadable term list", "retired-term")
	if err := os.Remove(agentMemLintTermsPath()); err != nil {
		t.Fatal(err)
	}
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "release-steps" {
		t.Errorf("with no list file the memory qualifies again: %+v %v", w.Held, w.Skipped)
	}
}

// A partial clone: lint's reads must not fetch the blobs it lacks. With lazy fetching off, a
// blob:none clone is refused by the partial-clone check before git grep runs (grep would pass over
// the missing blobs silently), and in a tree:0 clone the file list itself fails; either holds the
// memory as unverified, no upload-pack is run and no object is written. A partial clone whose
// tree a checkout completed is searched as usual.
func TestAgentMemoryPromotePartialCloneFetchesNothing(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n", "main.go": "package main\nfunc releaseAll() {}\n"})
	origin := filepath.Join(home, "origin.git")
	if err := agentMemGit(home, "clone", "--quiet", "--bare", clone, origin); err != nil {
		t.Fatal(err)
	}
	if err := agentMemGit(origin, "config", "uploadpack.allowFilter", "true"); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(home, "upload-pack-calls")
	script := filepath.Join(home, "upload-pack.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+calls+"\nexec git-upload-pack \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	partial := func(name, filter string) (string, agentMemCaller) {
		t.Helper()
		dir := filepath.Join(home, "repos", name)
		if err := agentMemGit(home, "clone", "--quiet", "--filter="+filter, "--no-checkout", "file://"+origin, dir); err != nil {
			t.Fatal(err)
		}
		if err := agentMemGit(dir, "config", "remote.origin.uploadpack", script); err != nil {
			t.Fatal(err)
		}
		s1, s2 := "claude-"+name, "codex-"+name
		session.WriteMeta(session.Meta{Name: s1, Dir: dir, Kind: "claude"})
		session.WriteMeta(session.Meta{Name: s2, Dir: dir, Kind: "codex"})
		a, b := agentMemCallerT(t, s1), agentMemCallerT(t, s2)
		promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh via `releaseAll`", "scripts/release.sh", "releaseAll")
		promoteConfirm(t, "release-steps", 1, a, b)
		return dir, a
	}
	for _, tc := range []struct{ name, filter, skipped string }{
		{"blobless", "blob:none", "symbol checks: the default branch's files are not all present"},
		{"treeless", "tree:0", "repository checks"},
	} {
		dir, a := partial(tc.name, tc.filter)
		_ = os.Remove(calls)
		before := agentMemLintSnapshot(t, filepath.Join(dir, ".git", "objects"))
		w := promoteGet(t, "project="+a.Project.ID)
		if len(w.Candidates) != 0 || len(w.Skipped) != 1 || !strings.HasPrefix(w.Skipped[0], tc.skipped) {
			t.Errorf("%s: candidates %v, skipped %v", tc.filter, promoteNames(w), w.Skipped)
		}
		if got := promoteHeldRules(w, "release-steps"); len(got) != 1 || !strings.HasPrefix(got[0], memorypromote.RuleUnverified+"=") {
			t.Errorf("%s: held %v", tc.filter, got)
		}
		if _, err := os.Stat(calls); err == nil {
			t.Errorf("%s: the run reached the remote (upload-pack ran)", tc.filter)
		}
		if !reflectEq(before, agentMemLintSnapshot(t, filepath.Join(dir, ".git", "objects"))) {
			t.Errorf("%s: the run wrote objects into the clone", tc.filter)
		}
	}
	// Positive control: the same clone does fetch when lazy fetching is on, so the check above
	// can see a fetch at all.
	_ = os.Remove(calls)
	if err := agentMemGit(filepath.Join(home, "repos", "blobless"), "cat-file", "blob", "HEAD:main.go"); err != nil {
		t.Fatalf("control fetch: %v", err)
	}
	if _, err := os.Stat(calls); err != nil {
		t.Error("control: a lazy fetch did not run upload-pack, so the test cannot see one")
	}

	// A blob:none clone that has fetched only the file holding the symbol, with its promisor
	// remote declared as a bare key (`promisor`, which git reads as true): the tree is still
	// incomplete, so the symbol search is not trusted.
	dir, a := partial("someblobs", "blob:none")
	if err := agentMemGit(dir, "cat-file", "blob", "origin/HEAD:main.go"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(raw), "promisor = true") {
		t.Fatalf("fixture config: %v\n%s", err, raw)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(raw), "promisor = true", "promisor", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	w := promoteGet(t, "project="+a.Project.ID)
	if len(w.Candidates) != 0 || len(w.Skipped) != 1 || !strings.HasPrefix(w.Skipped[0], "symbol checks: the default branch's files are not all present") {
		t.Errorf("some blobs: candidates %v, skipped %v", promoteNames(w), w.Skipped)
	}

	// A blob:none clone with the default branch checked out has the whole tree: lint searches its
	// symbols as before, and the memory qualifies.
	dir, a = partial("complete", "blob:none")
	if err := agentMemGit(dir, "checkout", "--quiet"); err != nil {
		t.Fatal(err)
	}
	lw := agentMemLintGet(t, "scope=project&project="+a.Project.ID)
	if len(lw.Report.Findings) != 0 || strings.Contains(strings.Join(lw.Report.Skipped, ";"), "symbol checks") {
		t.Errorf("complete partial clone, lint: findings %+v, skipped %v", lw.Report.Findings, lw.Report.Skipped)
	}
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "release-steps" {
		t.Errorf("complete partial clone: held %+v, skipped %v", w.Held, w.Skipped)
	}
}

// Lint's removed-key history reads `git log -p`; a textconv driver configured in the repository
// (with cachetextconv, which writes notes) must not run for it.
func TestAgentMemoryPromoteRunsNoTextconv(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	conv := filepath.Join(home, "textconv-calls")
	script := filepath.Join(home, "textconv.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+conv+"\ncat \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "diff.rec.textconv", script}, {"config", "diff.rec.cachetextconv", "true"}} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatal(err)
		}
	}
	// info/attributes, because lint reads by --git-dir without a work tree, where a tree's
	// .gitattributes is not read but the repository's own attributes are.
	if err := os.MkdirAll(filepath.Join(clone, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".git", "info", "attributes"), []byte("*.txt diff=rec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentMemLintOrigin(t, clone, map[string]string{"notes.txt": "AF_OLD_SWITCH=1\n", "scripts/release.sh": "#!/bin/sh\n"})
	agentMemLintOrigin(t, clone, map[string]string{"notes.txt": "nothing\n"})
	a, b := agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	_ = os.Remove(conv)
	objects := agentMemLintSnapshot(t, filepath.Join(clone, ".git", "objects"))
	if w := promoteGet(t, "project="+a.Project.ID); strings.Join(promoteNames(w), ",") != "release-steps" {
		t.Fatalf("held %+v, skipped %v", w.Held, w.Skipped)
	}
	if _, err := os.Stat(conv); err == nil {
		t.Error("the run ran the textconv driver")
	}
	if out := agentMemGitOut(t, clone, "for-each-ref", "refs/notes"); strings.TrimSpace(out) != "" {
		t.Errorf("the run wrote notes: %s", out)
	}
	if !reflectEq(objects, agentMemLintSnapshot(t, filepath.Join(clone, ".git", "objects"))) {
		t.Error("the run wrote objects")
	}
	// Positive control: a plain `git log -p` over the same history does run the driver.
	ctl := exec.Command("git", "--git-dir="+filepath.Join(clone, ".git"), "log", "-p", "-n", "2", "--format=%h", "refs/remotes/origin/main")
	// cachetextconv commits notes, so the control needs an identity.
	ctl.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := ctl.CombinedOutput(); err != nil {
		t.Fatalf("control: %v %s", err, out)
	}
	if _, err := os.Stat(conv); err != nil {
		t.Error("control: git log -p did not run the driver, so the test cannot see one")
	}
}

// The route takes one project and refuses everything else with the matching status.
func TestAgentMemoryPromoteCandidatesRefusesBadInput(t *testing.T) {
	agentMemTestEnv(t)
	a := agentMemCallerT(t, "claude-main")
	promoteSave(t, a, "m", "lesson", "x")
	p := "project=" + a.Project.ID
	for q, want := range map[string]int{
		"":                              http.StatusBadRequest,
		"project=":                      http.StatusBadRequest,
		"project=Not%20An%20Id":         http.StatusBadRequest,
		"project=nope-0123456789ab":     http.StatusNotFound,
		"scope=user&" + p:               http.StatusBadRequest,
		p + "&days=":                    http.StatusBadRequest,
		p + "&days=0":                   http.StatusBadRequest,
		p + "&days=91":                  http.StatusBadRequest,
		p + "&days=x":                   http.StatusBadRequest,
		p + "&days=+5":                  http.StatusBadRequest,
		p + "&days=%zz":                 http.StatusBadRequest,
		p + "&days=1":                   http.StatusOK,
		p + "&days=90":                  http.StatusOK,
		p + "&days=08":                  http.StatusOK,
		"scope=project&" + p:            http.StatusOK,
		p + "&days=7&days=0":            http.StatusBadRequest,
		"scope=project&scope=user&" + p: http.StatusBadRequest,
		p + "&" + p:                     http.StatusBadRequest,
	} {
		if code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/promotion-candidates?"+q, nil); code != want {
			t.Errorf("%q: %d, want %d (%s)", q, code, want, b)
		}
	}
	if w := promoteGet(t, p+"&days=08"); w.QuietDays != 8 {
		t.Errorf("days=08 read as %d", w.QuietDays)
	}
}
