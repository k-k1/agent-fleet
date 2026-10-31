package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

// decoys makes n project directories next to the fixture's own, so a sweep reads what it reads
// in production.
func (f *skipFixture) decoys(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		if err := os.MkdirAll(filepath.Join(filepath.Dir(f.proj), fmt.Sprintf("decoy-%03d", i)), 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
}

// sweeps runs one fold pass and returns how many projects/* sweeps it made.
func (f *skipFixture) sweeps() int64 {
	f.t.Helper()
	before := claude.ProjectSweeps()
	f.fold()
	return claude.ProjectSweeps() - before
}

// A session that never produced a transcript costs its sweeps once; the second pass costs none
// (#3116). The first pass counting more than zero shows the seam sees the sweeps at all.
func TestFoldMissArchivedTranscriptlessCostsNoSweepOnSecondPass(t *testing.T) {
	f := newSkipFixture(t)
	f.decoys(20)
	f.m.Archived = true
	if got := f.sweeps(); got < 2 {
		t.Fatalf("first pass made %d sweeps; the counting seam should see the main and subagent sweeps", got)
	}
	for i := 0; i < 3; i++ {
		if got := f.sweeps(); got != 0 {
			t.Fatalf("pass %d after the miss made %d sweeps, want 0", i+2, got)
		}
	}
}

// A subagent transcript that shows up after a remembered miss is folded once the memo no longer
// holds. Archiving or restoring changes the liveness the memo is keyed on, which is one of the
// two ways it ends (the other is its TTL, pinned in the claude package).
func TestFoldMissSubagentAppearingLaterIsFolded(t *testing.T) {
	f := newSkipFixture(t)
	f.decoys(5)
	f.m.Archived = true
	f.fold()
	f.fold()
	agent := filepath.Join(f.proj, f.sid, "subagents", "agent-late.jsonl")
	f.write(agent, claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "a1", 2000, 20))
	if f.fold(); true {
		if _, side := f.rows(); side != 0 {
			t.Fatalf("a held miss still folded %d sidechain rows; the test no longer exercises the memo", side)
		}
	}
	f.m.Archived = false // restored: the remembered miss is discarded
	f.fold()
	if _, side := f.rows(); side != 1 {
		t.Fatalf("sidechain rows = %d after the memo ended, want 1", side)
	}
}

// The settling fold (deletion, archiving) never trusts the memo.
func TestFoldMissIgnoredWhenSettling(t *testing.T) {
	f := newSkipFixture(t)
	f.m.Archived = true
	f.fold()
	f.write(filepath.Join(f.proj, f.sid, "subagents", "agent-late.jsonl"),
		claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "a1", 2000, 20))
	if _, err := commitSessionUsageFold(f.m, true); err != nil {
		t.Fatal(err)
	}
	if _, side := f.rows(); side != 1 {
		t.Fatalf("sidechain rows = %d after a settling fold, want 1", side)
	}
}
