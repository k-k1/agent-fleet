package gitx

import "testing"

// The overlap rules of BeginDelete (#4833): the same copy, a worktree and its base clone never
// run together; unrelated copies do, and a mark is gone once its delete ends.
func TestBeginDeleteRules(t *testing.T) {
	endA, ok, _ := BeginDelete("/r/app@a", "/r/app")
	if !ok {
		t.Fatal("first delete refused")
	}
	if _, ok, _ := BeginDelete("/r/app@a", "/r/app"); ok {
		t.Fatal("the same copy was marked twice")
	}
	endB, ok, _ := BeginDelete("/r/app@b", "/r/app")
	if !ok {
		t.Fatal("a sibling worktree was refused")
	}
	if _, ok, _ := BeginDelete("/r/app", ""); ok {
		t.Fatal("the base clone was marked while its worktrees were being deleted")
	}
	if !UnderDeletion("/r/app@a") || !UnderDeletion("/r/app@a/sub") || UnderDeletion("/r/app@a2") || UnderDeletion("/r/app") {
		t.Fatal("UnderDeletion does not follow the marks")
	}
	endA()
	endA() // idempotent
	endB()
	endBase, ok, _ := BeginDelete("/r/app", "")
	if !ok {
		t.Fatal("the base clone is still refused after its worktrees' deletes ended")
	}
	if _, ok, _ := BeginDelete("/r/app@c", "/r/app"); ok {
		t.Fatal("a worktree was marked while its base clone was being deleted")
	}
	endBase()
	if UnderDeletion("/r/app") {
		t.Fatal("mark left behind")
	}
}
