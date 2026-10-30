package memreview

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// rejectedAbout queues, rejects and returns a proposal about the user-scope memory name.
func rejectedAbout(t *testing.T, n int, name string, at time.Time) Proposal {
	t.Helper()
	p := uprop(n, apRun1, OpUpdate, name, "an idea nobody wanted "+name, 1)
	queue(t, p)
	if err := Reject(p.ID, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func forgetThroughApproval(t *testing.T, n int, name string) {
	t.Helper()
	f := uprop(n, apRun2, OpForget, name, "", 1)
	queue(t, f)
	if _, err := Approve(f.ID, apNow); err != nil {
		t.Fatal(err)
	}
}

func seedMemory(t *testing.T, n int, name string) {
	t.Helper()
	s := uprop(n, apRun1, OpCreate, name, "v1", 0)
	queue(t, s)
	if _, err := Approve(s.ID, apNow); err != nil {
		t.Fatal(err)
	}
}

// A release that cannot be done when the memory is forgotten is not lost: the forget still
// succeeds, the failure is visible, and the next wake-up finishes it. A rejection made after the
// forget is about the memory as it is then and stays.
func TestAFailedReleaseIsRetriedAndKeepsLaterRejections(t *testing.T) {
	approvalEnv(t)
	memoryx.SetForgetHooks(ForgetHooks())
	seedMemory(t, 1, "topic")
	old := rejectedAbout(t, 2, "topic", time.Now().Add(-time.Hour))

	// The rejected directory cannot be written to.
	if err := os.Chmod(rejectedStore.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rejectedStore.Dir(), 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode")
	}
	forgetThroughApproval(t, 3, "topic")
	if !Rejected(old.Hash) {
		t.Fatal("the release should have failed in this setup (the control did not hold)")
	}
	if ReleasePending() != 1 {
		t.Fatalf("the failure is not visible: pending releases = %d", ReleasePending())
	}

	if err := os.Chmod(rejectedStore.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Someone rejects an idea about the forgotten memory after the forget.
	later := uprop(4, apRun2, OpCreate, "topic", "proposed again", 0)
	queue(t, later)
	if err := Reject(later.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	ReconcileReleases()
	if Rejected(old.Hash) {
		t.Fatal("the owed release was not finished")
	}
	if !Rejected(later.Hash) {
		t.Fatal("a rejection made after the forget was released with the old ones")
	}
	if ReleasePending() != 0 {
		t.Fatal("a finished release is still owed")
	}
}

// hooksWithoutDone installs the intent half of the hooks only, and records the base each forget
// carried: what a stop between the commit and the "done" call leaves behind.
func hooksWithoutDone(t *testing.T) *[]string {
	t.Helper()
	var bases []string
	memoryx.SetForgetHooks(&memoryx.ForgetHooks{Intent: func(scope, pid, name, base string) error {
		bases = append(bases, base)
		return noteForgetIntent(scope, pid, name, base)
	}})
	return &bases
}

// A stop between the forget's commit and the "done" call leaves the intent; the reconcile finishes
// it when the history shows the forget committed, and drops it when no forget did.
func TestReconcileTellsACommittedForgetFromOneThatNeverCommitted(t *testing.T) {
	approvalEnv(t)
	seedMemory(t, 1, "kept")
	seedMemory(t, 2, "gone")
	keptRej := rejectedAbout(t, 3, "kept", apNow)
	goneRej := rejectedAbout(t, 4, "gone", apNow)

	hooksWithoutDone(t)
	// "kept": an intent whose forget never committed (the process stopped before the commit).
	if err := noteForgetIntent("user", "", "kept", ""); err != nil {
		t.Fatal(err)
	}
	forgetThroughApproval(t, 5, "gone") // commits; the done call never comes
	if ReleasePending() != 2 {
		t.Fatalf("intents = %d", ReleasePending())
	}

	ReconcileReleases()
	if Rejected(goneRej.Hash) {
		t.Fatal("the hash of a memory that was forgotten was not released")
	}
	if !Rejected(keptRej.Hash) {
		t.Fatal("the hash of a memory that still exists was released")
	}
	if ReleasePending() != 0 {
		t.Fatalf("intents left = %d", ReleasePending())
	}
}

// With nothing remembered about a memory a forget writes nothing here: it does not depend on this
// package's directory, even when that directory cannot be written.
func TestNoIntentWithoutRejectionsAndNoDependenceOnTheDirectory(t *testing.T) {
	approvalEnv(t)
	memoryx.SetForgetHooks(ForgetHooks())
	seedMemory(t, 1, "plain")
	realRename := fsOps.rename
	fsOps.rename = func(from, to string) error {
		if strings.Contains(to, "/releasing/") {
			return errors.New("read-only")
		}
		return realRename(from, to)
	}
	t.Cleanup(func() { fsOps.rename = realRename })
	forgetThroughApproval(t, 2, "plain")
	if target(t, "plain").Exists || ReleasePending() != 0 {
		t.Fatal("a forget with nothing remembered was blocked or left a note")
	}
}

// ADR 0117 decision 5 needs the release to survive: when the note cannot be made durable the forget
// is refused with nothing changed (it would otherwise commit with nothing to recover the release
// from). All three paths that forget a memory are held to it.
func TestAForgetIsRefusedWhenTheOwedReleaseCannotBeRecorded(t *testing.T) {
	approvalEnv(t)
	memoryx.SetForgetHooks(ForgetHooks())
	seedMemory(t, 1, "topic")
	rej := rejectedAbout(t, 2, "topic", time.Now().Add(-time.Hour))
	realRename := fsOps.rename
	fsOps.rename = func(from, to string) error {
		if strings.Contains(to, "/releasing/") {
			return errors.New("read-only")
		}
		return realRename(from, to)
	}
	f := uprop(3, apRun2, OpForget, "topic", "", 1)
	queue(t, f)
	if _, err := Approve(f.ID, apNow); err == nil {
		t.Fatal("the forget went ahead without a durable note")
	}
	if !target(t, "topic").Exists || len(Pending()) != 1 {
		t.Fatal("the refused forget changed the store or left the queue")
	}
	// The same rule through the member's undo of the run that created it (the revert path).
	if rep, err := RevertRun(apRun1, apNow); err != nil || len(rep.Items) != 1 || rep.Items[0].Result == memoryx.ReviewRevertDone {
		t.Fatalf("revert = %+v %v", rep, err)
	}
	if !target(t, "topic").Exists {
		t.Fatal("the refused revert deleted the memory")
	}
	fsOps.rename = realRename
	// Once the directory works the forget goes through and the release is done.
	if _, err := Approve(f.ID, apNow); err != nil {
		t.Fatal(err)
	}
	if Rejected(rej.Hash) || ReleasePending() != 0 {
		t.Fatal("the release was not done")
	}
}

// An explicit save may re-create the memory after a forget that committed but whose release is
// still owed. The memory existing again is not evidence that the forget never happened, and a
// later forget attempt that fails does not overwrite what the first still owes.
func TestARecreationDoesNotEraseAnOwedRelease(t *testing.T) {
	approvalEnv(t)
	memoryx.AgentMemoryEnabled = func() bool { return true }
	t.Cleanup(func() { memoryx.AgentMemoryEnabled = nil })
	seedMemory(t, 1, "topic")
	old := rejectedAbout(t, 2, "topic", time.Now().Add(-time.Hour))

	hooksWithoutDone(t)
	forgetThroughApproval(t, 3, "topic") // committed; release owed
	if ReleasePending() != 1 {
		t.Fatalf("intents = %d", ReleasePending())
	}

	// A session saves the same name again (the ordinary path, not the review's).
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/agents/memory/entries",
		strings.NewReader(`{"scope":"user","name":"topic","description":"d","type":"feedback","body":"saved again"}`))
	r.Header.Set("Content-Type", "application/json")
	memoryx.HandleAgentMemorySave(w, r)
	if w.Code != 200 || !target(t, "topic").Exists {
		t.Fatalf("re-creation: %d %s", w.Code, w.Body)
	}

	// A second forget attempt of the new incarnation stops before its commit: its note is a
	// second intent, not an overwrite of the first.
	memoryx.SetForgetHooks(&memoryx.ForgetHooks{Intent: func(scope, pid, name, base string) error {
		_ = noteForgetIntent(scope, pid, name, base)
		return errors.New("stop")
	}})
	f2 := uprop(4, apRun2, OpForget, "topic", "", 2)
	queue(t, f2)
	if _, err := Approve(f2.ID, apNow); err == nil {
		t.Fatal("the stopped forget reported success")
	}
	if ReleasePending() != 2 {
		t.Fatalf("intents = %d, want the first forget's and the second's", ReleasePending())
	}

	ReconcileReleases()
	if Rejected(old.Hash) {
		t.Fatal("a re-created memory kept its predecessor's rejected hash")
	}
	if ReleasePending() != 0 {
		t.Fatalf("intents left = %d (the second forget never committed, so it owes nothing)", ReleasePending())
	}
}

// The reconcile reads the history without holding the lock; the intent it then acts on must be the
// one it read, not a newer one written under the same key meanwhile.
func TestSettleLeavesANewerIntentAlone(t *testing.T) {
	approvalEnv(t)
	in := releaseIntent{Scope: "user", Name: "n", Base: "b", At: time.Now().Add(-time.Minute)}
	key := releaseKey(in.Scope, in.ProjectID, in.Name, in.Base)
	if err := releaseStore.Write(key, in); err != nil {
		t.Fatal(err)
	}
	newer := in
	newer.At = time.Now()
	if err := releaseStore.Write(key, newer); err != nil {
		t.Fatal(err)
	}
	settleIntent(key, in, false) // the stale snapshot says "never committed"
	if ReleasePending() != 1 {
		t.Fatal("a newer intent was removed with an old snapshot")
	}
	settleIntent(key, newer, false)
	if ReleasePending() != 0 {
		t.Fatal("the matching snapshot did not settle it")
	}
}
