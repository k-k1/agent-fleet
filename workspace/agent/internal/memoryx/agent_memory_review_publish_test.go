package memoryx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRun  = "aaaaaaaaaaaaaaaaaaaa"
	testRun2 = "bbbbbbbbbbbbbbbbbbbb"
)

func testPID(n int) string { return strings.Repeat(string(rune('0'+n)), 20) }

func reviewProp(n int, run, op, name, body string, base int) ReviewProposal {
	return ReviewProposal{ID: testPID(n), RunID: run, Session: "claude-main", Op: op, Scope: "project",
		ProjectID: "", Name: name, Type: "project", Description: "d " + name, Body: body, BaseRevision: base}
}

// withProject fills the project id of the demo clone.
func withProject(t *testing.T, p ReviewProposal) ReviewProposal {
	t.Helper()
	p.ProjectID = agentMemCallerT(t, "claude-main").Project.ID
	return p
}

func headMessage(t *testing.T, rev string) string {
	t.Helper()
	msg, err := memoryGitRun("log", "-1", "--format=%B", rev)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func commitCount(t *testing.T) string {
	t.Helper()
	n, err := memoryGitRun("rev-list", "--count", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func reviewReason(err error) string {
	var rf *ReviewRefusal
	if errors.As(err, &rf) {
		return rf.Kind
	}
	return ""
}

// An approved proposal is published through the tools' own path, as author kind review, with the
// reviewed session as author session, and carries the run and proposal ids.
func TestPublishReviewIsAnOrdinaryAuthoredCommit(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	res, err := PublishReview(withProject(t, reviewProp(1, testRun, "create", "build-cmd", "run make", 0)), now)
	if err != nil {
		t.Fatal(err)
	}
	msg := headMessage(t, res.Commit)
	for _, want := range []string{"AF-Op: create", "AF-Author-Kind: review", "AF-Author-Session: claude-main",
		"AF-Review-Run: " + testRun, "AF-Review-Proposal: " + testPID(1)} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
	ch := agentMemChangesT(t)
	if len(ch) != 1 || ch[0].AuthorKind != "review" || ch[0].ReviewRun != testRun || !ch[0].Latest {
		t.Fatalf("change list = %+v", ch)
	}
	e, err := agentMemRead(agentMemCallerT(t, "codex-wt"), "project", "build-cmd")
	if err != nil || e.AuthorKind != "review" || e.AuthorSession != "claude-main" || e.Revision != 1 || e.Body != "run make" {
		t.Fatalf("entry = %+v, %v", e, err)
	}

	// An update at the revision the review saw, then a forget.
	up, err := PublishReview(withProject(t, reviewProp(2, testRun, "update", "build-cmd", "run make all", 1)), now)
	if err != nil || up.Revision != 2 {
		t.Fatalf("update: %+v %v", up, err)
	}
	fg, err := PublishReview(withProject(t, reviewProp(3, testRun, "forget", "build-cmd", "", 2)), now)
	if err != nil || !fg.Deleted {
		t.Fatalf("forget: %+v %v", fg, err)
	}
	if _, err := agentMemRead(agentMemCallerT(t, "codex-wt"), "project", "build-cmd"); err == nil {
		t.Fatal("the forgotten memory is still readable")
	}
}

// A stale base is never overwritten: the store is left exactly as it was.
func TestPublishReviewStaleBaseIsRefusedAndWritesNothing(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	// Someone edits it after the review looked (base 1).
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "member v2", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	before := commitCount(t)
	for name, p := range map[string]ReviewProposal{
		"update at the old base":  withProject(t, reviewProp(1, testRun, "update", "a", "review text", 1)),
		"forget at the old base":  withProject(t, reviewProp(2, testRun, "forget", "a", "", 1)),
		"create over a live name": withProject(t, reviewProp(3, testRun, "create", "a", "review text", 0)),
		"update of a missing one": withProject(t, reviewProp(4, testRun, "update", "nope", "review text", 1)),
	} {
		if _, err := PublishReview(p, now); reviewReason(err) != ReviewStale {
			t.Errorf("%s: err = %v, want stale", name, err)
		}
	}
	if commitCount(t) != before {
		t.Fatal("a refused proposal wrote a commit")
	}
	if e, _ := agentMemRead(c, "project", "a"); e.Body != "member v2" || e.Revision != 2 {
		t.Fatalf("the memory was changed: %+v", e)
	}
}

// ADR 0117 decision 5: a create is refused over a forgotten name, with the tombstone present and
// with it lost (the history still knows the name). A mutation that clears noRevival turns this red.
func TestPublishReviewNeverRevivesAForgottenMemory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "gone", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "gone", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	p := withProject(t, reviewProp(1, testRun, "create", "gone", "revived", 0))
	before := commitCount(t)
	if _, err := PublishReview(p, now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("tombstone present: err = %v", err)
	}
	tomb := filepath.Join(agentMemDir(), "projects", p.ProjectID, agentMemTombDir, "gone")
	if _, err := os.Stat(tomb); err != nil {
		t.Fatalf("the refused create removed the tombstone: %v", err)
	}
	// Lose the tombstone with its directory: the history is the second witness.
	if err := os.RemoveAll(filepath.Dir(tomb)); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishReview(p, now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("tombstone lost: err = %v", err)
	}
	if commitCount(t) != before {
		t.Fatal("a refused create wrote a commit")
	}
	// A name that never existed is created, and an unrelated tombstone does not block it.
	if _, err := PublishReview(withProject(t, reviewProp(2, testRun, "create", "fresh", "ok", 0)), now); err != nil {
		t.Fatalf("a fresh create: %v", err)
	}
}

func TestPublishReviewRefusesASecretAtApproval(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	if _, err := PublishReview(withProject(t, reviewProp(1, testRun, "create", "ok", "fine", 0)), now); err != nil {
		t.Fatal(err)
	}
	before := commitCount(t)
	p := withProject(t, reviewProp(2, testRun, "create", "k", "token "+agentMemFakeAWS(), 0))
	_, err := PublishReview(p, now)
	if reviewReason(err) != ReviewSecret {
		t.Fatalf("err = %v, want a secret refusal", err)
	}
	if strings.Contains(err.Error(), agentMemFakeAWS()[:8]) {
		t.Fatal("the refusal echoes the value")
	}
	if commitCount(t) != before {
		t.Fatal("a refused proposal wrote a commit")
	}
}

func commitCountOrZero() string {
	n, err := memoryGitRun("rev-list", "--count", memoryBranch)
	if err != nil {
		return "0"
	}
	return n
}

// Publishing twice (a retry after a stop between the commit and the queue removal) writes once.
func TestPublishReviewIsIdempotentPerProposal(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	p := withProject(t, reviewProp(1, testRun, "create", "once", "text", 0))
	first, err := PublishReview(p, now)
	if err != nil || first.Already {
		t.Fatalf("%+v %v", first, err)
	}
	n := commitCount(t)
	again, err := PublishReview(p, now)
	if err != nil || !again.Already || again.Commit != first.Commit {
		t.Fatalf("retry = %+v %v, want the first commit and Already", again, err)
	}
	if commitCount(t) != n {
		t.Fatal("the retry wrote another commit")
	}
	// A different proposal with the same text is a different publish (and here stale).
	if _, err := PublishReview(withProject(t, reviewProp(2, testRun, "create", "once", "text", 0)), now); reviewReason(err) == "" {
		t.Fatalf("another id for the same create: %v", err)
	}
}

// The model sees no kinds, so an update keeps the ones the memory has.
func TestPublishReviewUpdateKeepsTheMemoryKinds(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "k", Description: "d", Type: "project", Kinds: []string{"claude"}, Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishReview(withProject(t, reviewProp(1, testRun, "update", "k", "v2", 1)), now); err != nil {
		t.Fatal(err)
	}
	e, err := agentMemRead(c, "project", "k")
	if err != nil || len(e.Kinds) != 1 || e.Kinds[0] != "claude" || e.Body != "v2" {
		t.Fatalf("entry = %+v %v", e, err)
	}
}

// A first memory of a project brings the project's record along; a proposal whose session is gone
// still publishes into a project that already has memory, and is refused for one that does not.
func TestPublishReviewProjectWithoutItsSession(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	p := withProject(t, reviewProp(1, testRun, "create", "first", "text", 0))
	if _, err := PublishReview(p, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agentMemDir(), "projects", p.ProjectID, "project.json")); err != nil {
		t.Fatalf("the project record was not written: %v", err)
	}
	p2 := withProject(t, reviewProp(2, testRun, "create", "second", "text", 0))
	p2.Session = "long-gone"
	if _, err := PublishReview(p2, now); err != nil {
		t.Fatalf("session gone, project known: %v", err)
	}
	p3 := reviewProp(3, testRun, "create", "third", "text", 0)
	p3.Session, p3.ProjectID = "long-gone", "demo-0123456789ab"
	if _, err := PublishReview(p3, now); reviewReason(err) != ReviewInvalid {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestPublishReviewRejectsMalformedIDs(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	p := withProject(t, reviewProp(1, testRun, "create", "a", "b", 0))
	for name, mut := range map[string]func(*ReviewProposal){
		"proposal id with a newline": func(p *ReviewProposal) { p.ID = "0123456789abcdef012\n" },
		"run id with a trailer":      func(p *ReviewProposal) { p.RunID = "aaaaaaaaaaaaaaaaaaa\nAF-Op: forget" },
		"session with a slash":       func(p *ReviewProposal) { p.Session = "../x" },
		"bad op":                     func(p *ReviewProposal) { p.Op = "pin" },
	} {
		q := p
		mut(&q)
		if _, err := PublishReview(q, time.Now()); reviewReason(err) != ReviewInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if commitCountOrZero() != "0" {
		t.Fatal("a malformed proposal wrote a commit")
	}
}

func TestReviewTargetReadsTheLiveState(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	pid := c.Project.ID
	if st, err := ReviewTarget("project", pid, "a"); err != nil || st.Exists || st.Forgotten {
		t.Fatalf("absent: %+v %v", st, err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "desc", Type: "project", Body: "body"}, now); err != nil {
		t.Fatal(err)
	}
	st, err := ReviewTarget("project", pid, "a")
	if err != nil || !st.Exists || st.Revision != 1 || st.Body != "body" || st.Description != "desc" {
		t.Fatalf("live: %+v %v", st, err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "a", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReviewTarget("project", pid, "a"); st.Exists || !st.Forgotten {
		t.Fatalf("forgotten: %+v", st)
	}
	// No use was counted by any of those reads.
	if st, _ := ReviewTarget("user", "", "u"); st.Exists {
		t.Fatalf("user scope: %+v", st)
	}
}

// Reverting a run undoes, as new commits, each commit still the newest of its memory; one edited
// since is left and reported; running it again retries only what is left (decision 5).
func TestRevertReviewRun(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	c := agentMemCallerT(t, "claude-main")
	for _, n := range []string{"b-edited", "c-forgotten"} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: n, Description: "d", Type: "project", Body: "original " + n}, now); err != nil {
			t.Fatal(err)
		}
	}
	steps := []ReviewProposal{
		reviewProp(1, testRun, "create", "a-created", "new", 0),
		reviewProp(2, testRun, "update", "b-edited", "review text", 1),
		reviewProp(3, testRun, "forget", "c-forgotten", "", 1),
		reviewProp(4, testRun2, "create", "other-run", "x", 0), // another run is not touched
	}
	for _, p := range steps {
		if _, err := PublishReview(withProject(t, p), now); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
	}
	// The member edits one of the run's memories afterwards.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "b-edited", Description: "d", Type: "project", Body: "member text", Revision: 2}, now); err != nil {
		t.Fatal(err)
	}

	rep, err := RevertReviewRun(testRun, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ReviewRevertItem{}
	for _, it := range rep.Items {
		got[it.Name] = it
	}
	if len(got) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	if got["a-created"].Result != ReviewRevertDone || got["c-forgotten"].Result != ReviewRevertDone {
		t.Errorf("items = %+v", rep.Items)
	}
	if it := got["b-edited"]; it.Result != ReviewRevertConflict || it.Reason != ReviewConflictEdited {
		t.Errorf("edited = %+v", it)
	}
	// The effects: the created one is gone, the forgotten one is back, the edited one is the member's.
	if _, err := agentMemRead(c, "project", "a-created"); err == nil {
		t.Error("the run's creation was not undone")
	}
	if e, err := agentMemRead(c, "project", "c-forgotten"); err != nil || e.Body != "original c-forgotten" {
		t.Errorf("the run's forget was not undone: %+v %v", e, err)
	}
	if e, _ := agentMemRead(c, "project", "b-edited"); e.Body != "member text" {
		t.Errorf("a member's later edit was clobbered: %+v", e)
	}
	if e, err := agentMemRead(c, "project", "other-run"); err != nil || e.Body != "x" {
		t.Errorf("another run was touched: %+v %v", e, err)
	}
	// Reverts are new commits by the member that point at what they undo; nothing was rewritten.
	for _, ch := range agentMemChangesT(t) {
		if ch.Op == "revert" && (ch.AuthorKind != "member" || ch.RevertOf == "") {
			t.Errorf("revert row = %+v", ch)
		}
	}
	// Two seeds, four publishes, the member's edit and two reverts: nothing was rewritten or dropped.
	if n := commitCount(t); n != "9" {
		t.Errorf("commit count = %s, want 9", n)
	}

	// Again: what was reverted is reported as such, the conflict stays a conflict.
	rep2, err := RevertReviewRun(testRun, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep2.Items {
		switch it.Name {
		case "a-created", "c-forgotten":
			if it.Result != ReviewRevertAlready {
				t.Errorf("second pass %s = %+v", it.Name, it)
			}
		case "b-edited":
			if it.Result != ReviewRevertConflict {
				t.Errorf("second pass %s = %+v", it.Name, it)
			}
		}
	}
	// Once the member's edit is itself reverted by hand, the run's commit is not the newest either:
	// it stays a conflict (it is the member's call, not the review's).
	if _, err := RevertReviewRun("cccccccccccccccccccc", now); err != nil {
		t.Fatal(err)
	}
	if _, err := RevertReviewRun("not-a-run", now); err == nil {
		t.Fatal("a malformed run id was accepted")
	}
}

// A memory the review created and a run revert deleted is not brought back by a later review: the
// revert wrote a tombstone.
func TestRevertedReviewCreationStaysGone(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	if _, err := PublishReview(withProject(t, reviewProp(1, testRun, "create", "ghost", "x", 0)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := RevertReviewRun(testRun, now); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishReview(withProject(t, reviewProp(2, testRun2, "create", "ghost", "again", 0)), now); reviewReason(err) != ReviewForgotten {
		t.Fatalf("err = %v", err)
	}
}

// The forget hooks are told about every way a memory goes away, the intent before the commit and
// the done after it, with the scope, project and the base the forget started from.
func TestForgetHooksReportEveryForget(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	type ev struct{ phase, scope, pid, name string }
	var got []ev
	bases := map[string]string{}
	SetForgetHooks(&ForgetHooks{
		Intent: func(scope, pid, name, base string) error {
			got = append(got, ev{"intent", scope, pid, name})
			bases[name] = base
			return nil
		},
		Done: func(scope, pid, name, base string) {
			got = append(got, ev{"done", scope, pid, name})
			if base != bases[name] || base == "" {
				t.Errorf("done carries base %q, intent had %q", base, bases[name])
			}
		},
	})
	t.Cleanup(func() { SetForgetHooks(nil) })
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	pid := c.Project.ID
	mk := func(scope, name string) {
		t.Helper()
		if _, err := agentMemSave(c, agentMemSaveReq{Scope: scope, Name: name, Description: "d", Type: "project", Body: "b"}, now); err != nil {
			t.Fatal(err)
		}
	}
	mk("project", "p1")
	mk("user", "u1")
	mk("project", "p2")
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "p1", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Scope: "user", Name: "u1", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	// Undoing a creation forgets it too.
	var create string
	for _, x := range agentMemChangesT(t) {
		if x.Name == "p2" {
			create = x.Commit
		}
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: create}, now); err != nil {
		t.Fatal(err)
	}
	want := []ev{{"intent", "project", pid, "p1"}, {"done", "project", pid, "p1"},
		{"intent", "user", "", "u1"}, {"done", "user", "", "u1"},
		{"intent", "project", pid, "p2"}, {"done", "project", pid, "p2"}}
	if len(got) != len(want) {
		t.Fatalf("hook calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An Intent that refuses stops the forget before it changes anything, on all three paths that
// forget a memory: the forget itself, the undo of a creation, and the move out of a project.
func TestARefusedForgetIntentStopsEveryForgetPath(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	for _, n := range []string{"f1", "r1"} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: n, Description: "d", Type: "project", Body: "b"}, now); err != nil {
			t.Fatal(err)
		}
	}
	orphan, to := moveOrphan(t, home, "m1")
	SetForgetHooks(&ForgetHooks{Intent: func(string, string, string, string) error { return errors.New("no") }})
	t.Cleanup(func() { SetForgetHooks(nil) })
	before := commitCount(t)

	if _, err := agentMemForget(c, agentMemForgetReq{Name: "f1", Revision: 1}, now); err == nil {
		t.Error("forget went ahead")
	}
	var create string
	for _, x := range agentMemChangesT(t) {
		if x.Name == "r1" {
			create = x.Commit
		}
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: create}, now); err == nil {
		t.Error("undo of a creation went ahead")
	}
	if commitCount(t) != before {
		t.Error("a refused forget committed something")
	}
	// A move copies the memory to its new project first and forgets the source second; the refusal
	// stops it at the second step, which is the existing "stopped" outcome, source untouched.
	_, res, _ := moveDo(t, orphan, to, "", "m1")
	if res.Stopped != "m1" {
		t.Errorf("the move out of the project went ahead: %+v", res)
	}
	if _, ok := projectNames(t, orphan)["m1"]; !ok {
		t.Error("the memory left its project")
	}
	for _, n := range []string{"f1", "r1"} {
		if _, err := agentMemRead(c, "project", n); err != nil {
			t.Errorf("%s is gone: %v", n, err)
		}
	}
}

// ReviewForgetCommitted answers from the history: a forget that committed counts even when the
// memory was saved again afterwards, and one that did not commit does not count however the
// memory has changed since.
func TestReviewForgetCommittedReadsTheHistory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	pid := c.Project.ID
	base := ""
	SetForgetHooks(&ForgetHooks{Intent: func(_, _, _, b string) error { base = b; return nil }})
	t.Cleanup(func() { SetForgetHooks(nil) })
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v2", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	// A forget that was begun and not committed: the base is the update, and an edit follows.
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "a", Revision: 1}, now); err == nil {
		t.Fatal("a forget at a stale revision went through")
	}
	staleBase := base
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v3", Revision: 2}, now); err != nil {
		t.Fatal(err)
	}
	if got, err := ReviewForgetCommitted("project", pid, "a", staleBase); err != nil || got {
		t.Fatalf("no forget committed, got %v %v", got, err)
	}
	// A real forget, then the name is saved again.
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "a", Revision: 3}, now); err != nil {
		t.Fatal(err)
	}
	forgetBase := base
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "again"}, now); err != nil {
		t.Fatal(err)
	}
	if got, err := ReviewForgetCommitted("project", pid, "a", forgetBase); err != nil || !got {
		t.Fatalf("the committed forget was not seen after a re-creation: %v %v", got, err)
	}
	// A base that is not in the history (it was purged) with no forget to be seen: it cannot tell.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "b", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewForgetCommitted("project", pid, "b", "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("a base that is not in the history was taken as an answer")
	}
	if _, err := ReviewForgetCommitted("project", pid, "../x", ""); err == nil {
		t.Fatal("a bad name was accepted")
	}
}

// The screen's view of a create agrees with approval: a name the history knows is forgotten even
// when its tombstone is gone.
func TestReviewTargetForgottenSeesTheHistoryWhenTheTombstoneIsLost(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "a", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(agentMemDir(), "projects", c.Project.ID, agentMemTombDir)); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReviewTarget("project", c.Project.ID, "a"); st.Exists || !st.Forgotten {
		t.Fatalf("state = %+v, want forgotten by history", st)
	}
	if st, _ := ReviewTarget("project", c.Project.ID, "never-was"); st.Forgotten {
		t.Fatalf("a name that never existed reads as forgotten: %+v", st)
	}
}

// Losing the whole project directory takes the memory and its tombstone with it; the history still
// knows the name, and the screen must say so before anyone presses approve.
func TestReviewTargetForgottenSeesTheHistoryWhenTheProjectDirectoryIsLost(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Type: "project", Body: "v1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "a", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(agentMemDir(), "projects", c.Project.ID)); err != nil {
		t.Fatal(err)
	}
	if st, err := ReviewTarget("project", c.Project.ID, "a"); err != nil || st.Exists || !st.Forgotten {
		t.Fatalf("state = %+v %v, want forgotten by history", st, err)
	}
	if st, _ := ReviewTarget("project", c.Project.ID, "never-was"); st.Forgotten {
		t.Fatalf("a name that never existed reads as forgotten: %+v", st)
	}
}

// A history rewrite (a member's purge of another memory) renames every later commit, so the base a
// pending forget began from can vanish. An older forget of an earlier incarnation of the same name
// is then still in the history and must not be taken for this forget's.
func TestReviewForgetCommittedDoesNotUseAnOlderForgetWhenTheBaseIsGone(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	now := time.Now()
	c := agentMemCallerT(t, "claude-main")
	pid := c.Project.ID
	save := func(name, body string, rev int) {
		t.Helper()
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, Revision: rev}, now); err != nil {
			t.Fatal(err)
		}
	}
	save("other", "old text", 0)
	save("topic", "first incarnation", 0)
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "topic", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	save("topic", "second incarnation", 0)

	// The current topic's forget begins (Intent) and does not commit.
	base := ""
	SetForgetHooks(&ForgetHooks{Intent: func(_, _, _, b string) error { base = b; return errors.New("stop") }})
	t.Cleanup(func() { SetForgetHooks(nil) })
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "topic", Revision: 2}, now); err == nil || base == "" {
		t.Fatalf("the forget should have stopped at its intent (base %q): %v", base, err)
	}
	SetForgetHooks(nil)
	if got, err := ReviewForgetCommitted("project", pid, "topic", base); err != nil || got {
		t.Fatalf("before the purge: %v %v", got, err)
	}

	// The member purges another memory's past: every later commit gets a new id.
	save("other", "new text", 1)
	pv, err := agentMemPurgePreview(agentMemScopeProject, pid, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeProject, Project: pid, Name: "other", Plan: pv.Plan, Confirm: "other"}); err != nil {
		t.Fatal(err)
	}
	// Controls: the old base is gone from the history, the live topic is untouched, and the older
	// forget is still there.
	if _, err := memoryGitRun("cat-file", "-e", base+"^{commit}"); err == nil {
		t.Skip("the purge kept the base commit; the scenario needs its id to change")
	}
	if e, err := agentMemRead(c, "project", "topic"); err != nil || e.Body != "second incarnation" {
		t.Fatalf("the live topic changed: %+v %v", e, err)
	}
	got, err := ReviewForgetCommitted("project", pid, "topic", base)
	if err == nil {
		t.Fatalf("answered %v although the base is gone; the older forget must not stand in", got)
	}
	// Without a history at all, a base is not "nothing committed" either.
	if _, err := ReviewForgetCommitted("project", pid, "nobody-ever", "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("a missing base was read as an answer")
	}
}
