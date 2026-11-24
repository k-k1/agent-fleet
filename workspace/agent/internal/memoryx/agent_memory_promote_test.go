package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
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
	if len(w.Candidates) != 0 || strings.Join(promoteHeldRules(w, "m"), ";") != memorypromote.RuleUnpublished+"=" {
		t.Fatalf("candidates %v, held %v", promoteNames(w), promoteHeldRules(w, "m"))
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
	if err := os.WriteFile(agentMemLintTermsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("broken term list", "retired-term")
}

// A partial clone: lint's reads must not fetch the blobs it lacks. With lazy fetching off the
// symbol search fails (blob:none) or the file list does (tree:0); either holds the memory as
// unverified, no upload-pack is run and no object is written.
func TestAgentMemoryPromotePartialCloneFetchesNothing(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	promoteBackdate(t)
	agentMemLintOrigin(t, clone, map[string]string{"scripts/release.sh": "#!/bin/sh\n", "main.go": "package main\nfunc releaseAll() {}\n"})
	origin := filepath.Join(home, "origin.git")
	for _, args := range [][]string{{"clone", "--quiet", "--bare", clone, origin}} {
		if err := agentMemGit(home, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := agentMemGit(origin, "config", "uploadpack.allowFilter", "true"); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(home, "upload-pack-calls")
	script := filepath.Join(home, "upload-pack.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+calls+"\nexec git-upload-pack \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, filter := range []string{"blob:none", "tree:0"} {
		dir := filepath.Join(home, "repos", "part"+string(rune('a'+i)))
		if err := agentMemGit(home, "clone", "--quiet", "--filter="+filter, "--no-checkout", "file://"+origin, dir); err != nil {
			t.Fatal(err)
		}
		if err := agentMemGit(dir, "config", "remote.origin.uploadpack", script); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(calls)
		s1, s2 := "claude-"+filter[:4], "codex-"+filter[:4]
		session.WriteMeta(session.Meta{Name: s1, Dir: dir, Kind: "claude"})
		session.WriteMeta(session.Meta{Name: s2, Dir: dir, Kind: "codex"})
		a, b := agentMemCallerT(t, s1), agentMemCallerT(t, s2)
		promoteSave(t, a, "release-steps", "procedure", "run scripts/release.sh via `releaseAll`", "scripts/release.sh", "releaseAll")
		promoteConfirm(t, "release-steps", 1, a, b)
		before := agentMemLintSnapshot(t, filepath.Join(dir, ".git", "objects"))
		w := promoteGet(t, "project="+a.Project.ID)
		if len(w.Candidates) != 0 || len(w.Skipped) == 0 {
			t.Errorf("%s: candidates %v, skipped %v, held %v", filter, promoteNames(w), w.Skipped, promoteHeldRules(w, "release-steps"))
		}
		if _, err := os.Stat(calls); err == nil {
			t.Errorf("%s: the run reached the remote (upload-pack ran)", filter)
		}
		if !reflectEq(before, agentMemLintSnapshot(t, filepath.Join(dir, ".git", "objects"))) {
			t.Errorf("%s: the run wrote objects into the clone", filter)
		}
	}
	// Positive control: the same clone does fetch when lazy fetching is on, so the check above
	// can see a fetch at all.
	dir := filepath.Join(home, "repos", "parta")
	_ = os.Remove(calls)
	if err := agentMemGit(dir, "cat-file", "blob", "HEAD:main.go"); err != nil {
		t.Fatalf("control fetch: %v", err)
	}
	if _, err := os.Stat(calls); err != nil {
		t.Error("control: a lazy fetch did not run upload-pack, so the test cannot see one")
	}
}

// The route takes one project and refuses everything else with the matching status.
func TestAgentMemoryPromoteCandidatesRefusesBadInput(t *testing.T) {
	agentMemTestEnv(t)
	a := agentMemCallerT(t, "claude-main")
	promoteSave(t, a, "m", "lesson", "x")
	p := "project=" + a.Project.ID
	for q, want := range map[string]int{
		"":                          http.StatusBadRequest,
		"project=":                  http.StatusBadRequest,
		"project=Not%20An%20Id":     http.StatusBadRequest,
		"project=nope-0123456789ab": http.StatusNotFound,
		"scope=user&" + p:           http.StatusBadRequest,
		p + "&days=":                http.StatusBadRequest,
		p + "&days=0":               http.StatusBadRequest,
		p + "&days=91":              http.StatusBadRequest,
		p + "&days=x":               http.StatusBadRequest,
		p + "&days=+5":              http.StatusBadRequest,
		p + "&days=%zz":             http.StatusBadRequest,
		p + "&days=1":               http.StatusOK,
		p + "&days=90":              http.StatusOK,
		p + "&days=08":              http.StatusOK,
		"scope=project&" + p:        http.StatusOK,
	} {
		if code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/promotion-candidates?"+q, nil); code != want {
			t.Errorf("%q: %d, want %d (%s)", q, code, want, b)
		}
	}
	if w := promoteGet(t, p+"&days=08"); w.QuietDays != 8 {
		t.Errorf("days=08 read as %d", w.QuietDays)
	}
}
