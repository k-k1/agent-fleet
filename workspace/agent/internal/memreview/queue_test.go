package memreview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func queueEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
}

func prop(id, name, body string) Proposal {
	op := Op{Op: OpCreate, Scope: "project", Name: name, Type: "user", Description: "d", Body: body, Reason: "r", Evidence: []int{1}}
	return Proposal{ID: id, RunID: "run", Session: "s", ProjectID: "p", Op: op, Hash: OpHash("p", op), Created: time.Unix(100, 0)}
}

func TestQueueIsScannedAndIdempotent(t *testing.T) {
	queueEnv(t)
	if how, err := enqueue(prop("a", "n1", "text")); err != nil || how != EnqueuedNew {
		t.Fatalf("%q %v", how, err)
	}
	if how, _ := enqueue(prop("a", "n1", "text")); how != EnqueuedExisting {
		t.Fatalf("same id again: %q", how)
	}
	if how, _ := enqueue(prop("b", "n1", "text")); how != SkippedDuplicate {
		t.Fatalf("same idea under another id: %q", how)
	}
	if _, err := enqueue(prop("c", "n2", "uses "+fakeKey())); err == nil {
		t.Fatal("a proposal with a secret was queued")
	}
	if n := len(Pending()); n != 1 {
		t.Fatalf("pending = %d", n)
	}
	// Files are private to the user.
	_ = filepath.Walk(stateRoot(), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others (%v)", p, fi.Mode())
		}
		return nil
	})
}

// Decision 8: pending text is deleted on a decision; a rejection keeps only the hash, until the
// memory it concerned is forgotten.
func TestQueueRetention(t *testing.T) {
	queueEnv(t)
	p := prop("a", "keep-me", "SENTINEL-BODY-TEXT")
	if _, err := enqueue(p); err != nil {
		t.Fatal(err)
	}
	if err := Decide("a", DecisionReject, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if len(Pending()) != 0 {
		t.Fatal("the rejected proposal is still pending")
	}
	assertNoneContains(t, stateRoot(), "SENTINEL-BODY-TEXT")
	if !Rejected(p.Hash) {
		t.Fatal("the hash was not kept")
	}
	// The same idea is not proposed again, in any run.
	again := prop("z", "keep-me", "SENTINEL-BODY-TEXT")
	if how, _ := enqueue(again); how != SkippedRejected {
		t.Fatalf("re-proposal: %q", how)
	}
	// A different memory's forget does nothing; this memory's releases the hash.
	if n, _ := ForgetMemory("project", "other", "p"); n != 0 {
		t.Fatal("the hash was released by the wrong forget")
	}
	if n, _ := ForgetMemory("project", "keep-me", "other-project"); n != 0 || !Rejected(p.Hash) {
		t.Fatal("the hash was released by the wrong forget")
	}
	if n, err := ForgetMemory("project", "keep-me", "p"); n != 1 || err != nil || Rejected(p.Hash) {
		t.Fatal("forgetting the memory did not release its hash")
	}
	if how, _ := enqueue(again); how != EnqueuedNew {
		t.Fatalf("after the forget: %q", how)
	}
	// Approving removes the text and leaves no hash behind.
	if err := Decide("z", DecisionApprove, time.Unix(300, 0)); err != nil {
		t.Fatal(err)
	}
	if len(Pending()) != 0 || Rejected(again.Hash) {
		t.Fatal("approve left something behind")
	}
	if left := pendingStore.keys(); len(left) != 0 {
		t.Fatalf("pending files remain: %v", left)
	}
	for _, bad := range []struct{ id, d string }{{"missing", DecisionReject}, {"z", "maybe"}} {
		if Decide(bad.id, bad.d, time.Now()) == nil {
			t.Errorf("Decide(%q, %q) succeeded", bad.id, bad.d)
		}
	}
}

func TestOpHashIgnoresTheJustificationButNotTheContent(t *testing.T) {
	a := Op{Op: OpCreate, Scope: "user", Name: "n", Type: "user", Description: "d", Body: "b", Reason: "one", Evidence: []int{1}}
	b := a
	b.Reason, b.Evidence = "two", []int{9}
	if OpHash("p", a) != OpHash("p", b) {
		t.Error("the reason changes the idea's identity")
	}
	for name, c := range map[string]Op{"body": {Op: a.Op, Scope: a.Scope, Name: a.Name, Type: a.Type, Description: a.Description, Body: "other"},
		"name": {Op: a.Op, Scope: a.Scope, Name: "m", Type: a.Type, Description: a.Description, Body: a.Body},
		"op":   {Op: OpUpdate, Scope: a.Scope, Name: a.Name, Type: a.Type, Description: a.Description, Body: a.Body}} {
		if OpHash("p", c) == OpHash("p", a) {
			t.Errorf("a different %s hashes the same", name)
		}
	}
	if OpHash("p", a) == OpHash("q", a) {
		t.Error("the project is part of the identity")
	}
}

func TestWatermarkOnlyMovesForwardAndKeysAreIndependent(t *testing.T) {
	queueEnv(t)
	now := time.Unix(1, 0)
	if err := moveWatermark("a", 10, now); err != nil {
		t.Fatal(err)
	}
	_ = moveWatermark("a", 4, now)
	_ = moveWatermark("b", 2, now)
	if Watermark("a") != 10 || Watermark("b") != 2 || Watermark("c") != 0 {
		t.Fatalf("a=%d b=%d c=%d", Watermark("a"), Watermark("b"), Watermark("c"))
	}
}

func TestOldTerminalRunsArePrunedButOpenOnesAreNot(t *testing.T) {
	queueEnv(t)
	base := time.Unix(1000, 0)
	for i := 0; i < keepTerminalRuns+20; i++ {
		r := Run{ID: fmt.Sprintf("done%03d", i), Key: "k", State: StateDone}
		_ = saveRun(&r, base.Add(time.Duration(i)*time.Second))
	}
	open := Run{ID: "open-oldest", Key: "k2", State: StateClaimed}
	_ = saveRun(&open, base.Add(-time.Hour))
	pruneRuns(time.Now())
	n := 0
	for _, r := range runs() {
		if r.Terminal() {
			n++
		}
	}
	if n != keepTerminalRuns {
		t.Fatalf("terminal runs = %d", n)
	}
	if _, ok := unfinishedRun("k2"); !ok {
		t.Fatal("an open run was pruned")
	}
	if strings.Contains(fmt.Sprint(runs()), "done000") {
		t.Error("the oldest terminal run survived")
	}
}
