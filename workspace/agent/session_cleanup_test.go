package main

import "testing"

func TestClassifySessionCleanup(t *testing.T) {
	// Live → skipped entirely (working, not clutter).
	if _, _, _, ok := classifySessionCleanup(false, true); ok {
		t.Fatal("live session must not be a cleanup candidate")
	}
	// Stopped, not archived → propose archive, but review (stopped ≠ finished).
	action, safety, _, ok := classifySessionCleanup(false, false)
	if !ok || action != "archive_session" || safety != "review" {
		t.Fatalf("stopped: action=%q safety=%q ok=%v", action, safety, ok)
	}
	// Already archived → informational only (no assistant action; TTL-exempt).
	action, safety, _, ok = classifySessionCleanup(true, false)
	if !ok || action != "" || safety != "review" {
		t.Fatalf("archived: action=%q safety=%q ok=%v", action, safety, ok)
	}
}

func TestClassifyWorktreeCleanup(t *testing.T) {
	cases := []struct {
		name       string
		liveCount  int
		ahead      int
		dirty      bool
		relation   string
		wantAction string
		wantSafety string
	}{
		{"live session blocks", 1, 0, false, "contained", "", "keep"},
		{"dirty is protected", 0, 0, true, "contained", "", "keep"},
		{"ahead is protected", 0, 2, false, "contained", "", "keep"},
		{"merged clean is safe", 0, 0, false, "contained", "delete_worktree", "safe"},
		{"same as parent is safe", 0, 0, false, "same", "delete_worktree", "safe"},
		{"clean unmerged needs review", 0, 0, false, "unmerged", "delete_worktree", "review"},
		{"clean diverged needs review", 0, 0, false, "diverged", "delete_worktree", "review"},
		{"unknown relation reviews", 0, 0, false, "", "delete_worktree", "review"},
	}
	for _, c := range cases {
		action, safety, reason := classifyWorktreeCleanup(c.liveCount, c.ahead, c.dirty, c.relation)
		if action != c.wantAction || safety != c.wantSafety {
			t.Errorf("%s: action=%q safety=%q (want %q/%q)", c.name, action, safety, c.wantAction, c.wantSafety)
		}
		if reason == "" {
			t.Errorf("%s: empty reason", c.name)
		}
	}
}
