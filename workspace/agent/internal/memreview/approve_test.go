package memreview

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

const (
	apRun1 = "1111111111111111111a"
	apRun2 = "2222222222222222222b"
)

// approvalEnv is a private review state and memory store. The store's git must not detach a
// background gc that outlives the test's temp dir.
func approvalEnv(t *testing.T) {
	t.Helper()
	queueEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.autoDetach")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Cleanup(func() { memoryx.SetForgetHooks(nil) })
}

func apID(n int) string { return fmt.Sprintf("%019x%d", n, n%10) }

// uprop is a user-scope proposal (no session or project is needed to publish it).
func uprop(n int, run, op, name, body string, base int) Proposal {
	o := Op{Op: op, Scope: "user", Name: name, Reason: "because", Evidence: []int{1}, BaseRevision: base}
	if op != OpForget {
		o.Type, o.Description, o.Body = "feedback", "about "+name, body
	}
	return Proposal{ID: apID(n), RunID: run, Session: "a-session", Op: o, Hash: OpHash("", o), Created: time.Unix(int64(1000+n), 0)}
}

func queue(t *testing.T, ps ...Proposal) {
	t.Helper()
	for _, p := range ps {
		if how, err := enqueue(p); err != nil || how != EnqueuedNew {
			t.Fatalf("enqueue %s: %q %v", p.ID, how, err)
		}
	}
}

func target(t *testing.T, name string) memoryx.ReviewTargetState {
	t.Helper()
	st, err := memoryx.ReviewTarget("user", "", name)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func refusal(err error) string {
	var rf *memoryx.ReviewRefusal
	if errors.As(err, &rf) {
		return rf.Kind
	}
	return ""
}

var apNow = time.Now()

// Approving publishes the memory and leaves no copy of the proposal text anywhere in the review's
// state; the decided marker keeps a replaying run from queueing it again.
func TestApprovePublishesAndLeavesNoCopy(t *testing.T) {
	approvalEnv(t)
	p := uprop(1, apRun1, OpCreate, "build", "SENTINEL-APPROVED-TEXT", 0)
	queue(t, p)
	pub, err := Approve(p.ID, apNow)
	if err != nil || pub.Commit == "" || pub.Already {
		t.Fatalf("approve: %+v %v", pub, err)
	}
	if st := target(t, "build"); !st.Exists || st.Body != "SENTINEL-APPROVED-TEXT" || st.Revision != 1 {
		t.Fatalf("store = %+v", st)
	}
	if len(Pending()) != 0 || pendingCount() != 0 {
		t.Fatal("the approved proposal is still pending")
	}
	assertNoneContains(t, stateRoot(), "SENTINEL-APPROVED-TEXT")
	if how, _ := enqueue(p); how != SkippedDecided {
		t.Fatalf("a replay of the run queued it again: %q", how)
	}
	if _, err := Approve(p.ID, apNow); !errors.Is(err, ErrNoProposal) {
		t.Fatalf("approving twice: %v", err)
	}
}

// A memory edited since the review is never overwritten: the proposal is marked stale, keeps its
// text for the member, and a rejection then removes the text and keeps the hash.
func TestApproveOfAStaleBaseMarksItAndOverwritesNothing(t *testing.T) {
	approvalEnv(t)
	// The memory exists at revision 2: the review looked at revision 1.
	seed := uprop(1, apRun1, OpCreate, "style", "v1", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil {
		t.Fatal(err)
	}
	edit := uprop(2, apRun1, OpUpdate, "style", "member edit", 1)
	queue(t, edit)
	if _, err := Approve(edit.ID, apNow); err != nil {
		t.Fatal(err)
	}
	stale := uprop(3, apRun2, OpUpdate, "style", "SENTINEL-STALE-TEXT", 1)
	queue(t, stale)

	views := Proposals()
	if len(views) != 1 || views[0].State != StateStale || views[0].StaleReason != ReasonChanged || views[0].Current == nil || views[0].Current.Revision != 2 {
		t.Fatalf("the list does not show it stale before anyone presses approve: %+v", views)
	}
	if _, err := Approve(stale.ID, apNow); refusal(err) != memoryx.ReviewStale {
		t.Fatalf("approve: %v", err)
	}
	if st := target(t, "style"); st.Body != "member edit" || st.Revision != 2 {
		t.Fatalf("the memory was overwritten: %+v", st)
	}
	left := Pending()
	if len(left) != 1 || left[0].Stale != memoryx.ReviewStale {
		t.Fatalf("pending = %+v", left)
	}

	if err := Reject(stale.ID, apNow); err != nil {
		t.Fatal(err)
	}
	assertNoneContains(t, stateRoot(), "SENTINEL-STALE-TEXT")
	if !Rejected(stale.Hash) {
		t.Fatal("the rejected hash was not kept")
	}
}

// A create over a forgotten name is flagged, not published (decision 5, checked at approval).
func TestApproveRefusesToReviveAForgottenMemory(t *testing.T) {
	approvalEnv(t)
	seed := uprop(1, apRun1, OpCreate, "old", "v1", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil {
		t.Fatal(err)
	}
	gone := uprop(2, apRun1, OpForget, "old", "", 1)
	queue(t, gone)
	if _, err := Approve(gone.ID, apNow); err != nil {
		t.Fatal(err)
	}
	revive := uprop(3, apRun2, OpCreate, "old", "back again", 0)
	queue(t, revive)
	if v := Proposals(); len(v) != 1 || v[0].State != StateStale || v[0].StaleReason != memoryx.ReviewForgotten {
		t.Fatalf("view = %+v", v)
	}
	if _, err := Approve(revive.ID, apNow); refusal(err) != memoryx.ReviewForgotten {
		t.Fatalf("approve: %v", err)
	}
	if st := target(t, "old"); st.Exists {
		t.Fatalf("the memory was revived: %+v", st)
	}
	if len(Pending()) != 1 {
		t.Fatal("a refused proposal left the queue without a decision")
	}
}

// A proposal that is published but whose queue entry survives (a stop in between, or a removal that
// failed) is finished by the next approval without a second commit.
func TestApproveRetryAfterTheDecisionCouldNotBeRecorded(t *testing.T) {
	approvalEnv(t)
	p := uprop(1, apRun1, OpCreate, "retry", "text", 0)
	queue(t, p)

	realRename := fsOps.rename
	fsOps.rename = func(from, to string) error {
		if strings.Contains(to, "/decided/") {
			return errors.New("disk full")
		}
		return realRename(from, to)
	}
	pub, err := Approve(p.ID, apNow)
	fsOps.rename = realRename
	if err == nil || pub.Commit == "" {
		t.Fatalf("expected a published result with an error: %+v %v", pub, err)
	}
	if len(Pending()) != 1 {
		t.Fatal("the proposal left the queue although the decision was not recorded")
	}

	again, err := Approve(p.ID, apNow)
	if err != nil || !again.Already || again.Commit != pub.Commit {
		t.Fatalf("retry = %+v %v, want the first commit", again, err)
	}
	if st := target(t, "retry"); st.Revision != 1 {
		t.Fatalf("the retry published again: %+v", st)
	}
	if len(Pending()) != 0 {
		t.Fatal("the retry left the proposal pending")
	}
}

func TestRejectAllPerRunAndAll(t *testing.T) {
	approvalEnv(t)
	a := uprop(1, apRun1, OpCreate, "a", "SENTINEL-A", 0)
	b := uprop(2, apRun1, OpCreate, "b", "SENTINEL-B", 0)
	c := uprop(3, apRun2, OpCreate, "c", "SENTINEL-C", 0)
	queue(t, a, b, c)
	if r := RejectAll(apRun1, apNow); r.Rejected != 2 || r.Failed != 0 {
		t.Fatalf("per run: %+v", r)
	}
	if left := Pending(); len(left) != 1 || left[0].ID != c.ID {
		t.Fatalf("pending = %+v", left)
	}
	for _, p := range []Proposal{a, b} {
		if !Rejected(p.Hash) {
			t.Errorf("%s: the hash was not kept", p.ID)
		}
	}
	if r := RejectAll("", apNow); r.Rejected != 1 {
		t.Fatalf("all: %+v", r)
	}
	for _, s := range []string{"SENTINEL-A", "SENTINEL-B", "SENTINEL-C"} {
		assertNoneContains(t, stateRoot(), s)
	}
	if r := RejectAll("", apNow); r.Rejected != 0 || r.Failed != 0 {
		t.Fatalf("empty: %+v", r)
	}
}

// A rejection whose hash cannot be written keeps the text (a decision never reports success with a
// copy left) and counts as failed; running it again completes it.
func TestRejectAllCountsAFailureAndRetries(t *testing.T) {
	approvalEnv(t)
	queue(t, uprop(1, apRun1, OpCreate, "a", "SENTINEL-KEPT", 0))
	realRename := fsOps.rename
	fsOps.rename = func(from, to string) error {
		if strings.Contains(to, "/rejected/") {
			return errors.New("read-only")
		}
		return realRename(from, to)
	}
	r := RejectAll("", apNow)
	fsOps.rename = realRename
	if r.Rejected != 0 || r.Failed != 1 || len(Pending()) != 1 {
		t.Fatalf("result = %+v, pending %d", r, len(Pending()))
	}
	if r := RejectAll("", apNow); r.Rejected != 1 || len(Pending()) != 0 {
		t.Fatalf("retry = %+v", r)
	}
}

// A rejected idea is remembered only as long as its memory: forgetting the memory (here by an
// approved forget, which goes through the same store path as every other forget) releases it.
func TestForgettingAMemoryReleasesItsRejectedHashes(t *testing.T) {
	approvalEnv(t)
	memoryx.SetForgetHooks(ForgetHooks())
	seed := uprop(1, apRun1, OpCreate, "topic", "v1", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil {
		t.Fatal(err)
	}
	rejected := uprop(2, apRun1, OpUpdate, "topic", "a rewrite the member did not want", 1)
	other := uprop(3, apRun1, OpCreate, "unrelated", "x", 0)
	queue(t, rejected, other)
	for _, id := range []string{rejected.ID, other.ID} {
		if err := Reject(id, apNow); err != nil {
			t.Fatal(err)
		}
	}
	forget := uprop(4, apRun2, OpForget, "topic", "", 1)
	queue(t, forget)
	if _, err := Approve(forget.ID, apNow); err != nil {
		t.Fatal(err)
	}
	if Rejected(rejected.Hash) {
		t.Fatal("the hash outlived the memory it concerned")
	}
	if !Rejected(other.Hash) {
		t.Fatal("forgetting one memory released another's hash")
	}
	// ... and without the hook installed nothing is released (the control for the check above).
	memoryx.SetForgetHooks(nil)
	again := uprop(5, apRun2, OpCreate, "topic2", "v1", 0)
	queue(t, again)
	if _, err := Approve(again.ID, apNow); err != nil {
		t.Fatal(err)
	}
	rej := uprop(6, apRun2, OpUpdate, "topic2", "nope", 1)
	queue(t, rej)
	if err := Reject(rej.ID, apNow); err != nil {
		t.Fatal(err)
	}
	f2 := uprop(7, apRun2, OpForget, "topic2", "", 1)
	queue(t, f2)
	if _, err := Approve(f2.ID, apNow); err != nil {
		t.Fatal(err)
	}
	if !Rejected(rej.Hash) {
		t.Fatal("the control did not hold: the hash was released with no hook")
	}
}

func TestRevertRunUndoesApprovedProposals(t *testing.T) {
	approvalEnv(t)
	a := uprop(1, apRun1, OpCreate, "a", "A", 0)
	b := uprop(2, apRun1, OpCreate, "b", "B", 0)
	queue(t, a, b)
	for _, p := range []Proposal{a, b} {
		if _, err := Approve(p.ID, apNow); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := RevertRun(apRun1, apNow)
	if err != nil || len(rep.Items) != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, it := range rep.Items {
		if it.Result != memoryx.ReviewRevertDone {
			t.Errorf("item = %+v", it)
		}
	}
	if target(t, "a").Exists || target(t, "b").Exists {
		t.Fatal("the run's memories are still there")
	}
}

func TestProposalViewsCarryTheLiveMemoryAndADiff(t *testing.T) {
	approvalEnv(t)
	seed := uprop(1, apRun1, OpCreate, "doc", "line one\nline two\nline three", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil {
		t.Fatal(err)
	}
	up := uprop(2, apRun2, OpUpdate, "doc", "line one\nline 2\nline three", 1)
	fg := uprop(3, apRun2, OpForget, "doc", "", 1)
	cr := uprop(4, apRun2, OpCreate, "fresh", "hello", 0)
	queue(t, up, fg, cr)
	by := map[string]ProposalView{}
	for _, v := range Proposals() {
		by[v.ID] = v
	}
	if v := by[up.ID]; v.State != StatePending || v.Current == nil || v.Current.Body != "line one\nline two\nline three" ||
		!strings.Contains(v.Diff, "-line two\n+line 2\n") || !strings.Contains(v.Diff, "--- a/af/user/doc.md\n+++ b/af/user/doc.md") {
		t.Errorf("update view = %+v", v)
	}
	if v := by[fg.ID]; v.Body != "" || !strings.Contains(v.Diff, "+++ /dev/null") || !strings.Contains(v.Diff, "-line two") {
		t.Errorf("forget view = %+v", v)
	}
	if v := by[cr.ID]; v.State != StatePending || v.Current != nil || !strings.Contains(v.Diff, "--- /dev/null") || !strings.Contains(v.Diff, "+hello") {
		t.Errorf("create view = %+v", v)
	}
}

func TestUnifiedDiff(t *testing.T) {
	lines := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("l%d", i))
		}
		return out
	}
	same := unifiedDiff("p", lines(5), true, lines(5), true)
	if strings.Contains(same, "@@") {
		t.Fatalf("identical texts produced a hunk:\n%s", same)
	}
	a, b := lines(20), lines(20)
	b[9] = "changed"
	d := unifiedDiff("p", a, true, b, true)
	for _, want := range []string{"@@ -7,7 +7,7 @@\n", " l7\n", "-l10\n", "+changed\n", " l13\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, " l6\n") || strings.Contains(d, " l14\n") {
		t.Errorf("more than 3 lines of context:\n%s", d)
	}
	// Two far-apart changes are two hunks.
	b[0] = "first"
	if got := strings.Count(unifiedDiff("p", a, true, b, true), "@@ -"); got != 2 {
		t.Errorf("hunks = %d", got)
	}
	// A whole new file.
	n := unifiedDiff("p", nil, false, []string{"x", "y"}, true)
	if !strings.Contains(n, "new file mode") || !strings.Contains(n, "@@ -0,0 +1,2 @@\n+x\n+y\n") {
		t.Errorf("new file:\n%s", n)
	}
	// Past the table cap it still produces a correct, if blunt, diff.
	big := make([]string, 2100)
	for i := range big {
		big[i] = fmt.Sprintf("row %d", i)
	}
	other := append([]string{"extra"}, big...)
	if bd := unifiedDiff("p", big, true, other, true); !strings.Contains(bd, "+extra\n") {
		t.Error("the capped diff lost the change")
	}
}

// The rules grow after a proposal was queued. Approval already refuses what matches them now; what
// is returned for display must be judged again too, as the published diffs are.
func TestAProposalThatMatchesTheRulesNowIsWithheldFromTheList(t *testing.T) {
	approvalEnv(t)
	p := uprop(1, apRun1, OpUpdate, "doc", "clean at first", 1)
	seed := uprop(2, apRun1, OpCreate, "doc", "the live text", 0)
	queue(t, seed)
	if _, err := Approve(seed.ID, apNow); err != nil {
		t.Fatal(err)
	}
	queue(t, p)
	// A later release of the scan knows a key the old one did not: simulated by editing the file.
	q, _ := pendingStore.Read(p.ID)
	q.Op.Body = "now with " + fakeKey()
	q.Op.Description = "also " + fakeKey()
	q.Op.Reason = "and " + fakeKey()
	if err := pendingStore.Write(p.ID, q); err != nil {
		t.Fatal(err)
	}
	// Positive control: approval does refuse it, so the check below looks at a real hit.
	if _, err := Approve(p.ID, apNow); refusal(err) != memoryx.ReviewSecret {
		t.Fatalf("approve: %v", err)
	}
	views := Proposals()
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if !v.Withheld || v.State != StateStale || v.StaleReason != memoryx.ReviewSecret {
		t.Fatalf("view = %+v", v)
	}
	b, _ := json.Marshal(views)
	if strings.Contains(string(b), fakeKey()[:8]) || v.Body != "" || v.Description != "" || v.Reason != "" || v.Diff != "" || v.Name != "" {
		t.Fatalf("the view carries the match: %s", b)
	}
	// The member can still get rid of it, and the text goes.
	if err := Reject(p.ID, apNow); err != nil {
		t.Fatal(err)
	}
	assertNoneContains(t, stateRoot(), fakeKey())
}

// The list agrees with approval about a forgotten name even when the tombstone is gone, and keeps a
// "forgotten" that an approval attempt recorded.
func TestListedCreateOverAForgottenNameIsForgottenWithoutItsTombstone(t *testing.T) {
	approvalEnv(t)
	seed := uprop(1, apRun1, OpCreate, "once", "v1", 0)
	gone := uprop(2, apRun1, OpForget, "once", "", 1)
	queue(t, seed, gone)
	for _, id := range []string{seed.ID, gone.ID} {
		if _, err := Approve(id, apNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "af-agent-memory", "user", ".forgotten")); err != nil {
		t.Fatal(err)
	}
	revive := uprop(3, apRun2, OpCreate, "once", "back", 0)
	queue(t, revive)
	// Before anyone presses approve (which would record the reason): the live state alone.
	if v := Proposals(); len(v) != 1 || v[0].State != StateStale || v[0].StaleReason != memoryx.ReviewForgotten {
		t.Fatalf("view = %+v", v)
	}
	// Positive control: the store refuses it, with the tombstone gone.
	if _, err := Approve(revive.ID, apNow); refusal(err) != memoryx.ReviewForgotten {
		t.Fatalf("approve: %v", err)
	}
	// A recorded "forgotten" is not thrown away when the live state alone would say pending.
	q, _ := pendingStore.Read(revive.ID)
	q.Op.Name = "never-existed"
	q.Stale = memoryx.ReviewForgotten
	if err := pendingStore.Write(revive.ID, q); err != nil {
		t.Fatal(err)
	}
	if v := Proposals(); len(v) != 1 || v[0].State != StateStale || v[0].StaleReason != memoryx.ReviewForgotten {
		t.Fatalf("recorded reason dropped: %+v", v)
	}
}
