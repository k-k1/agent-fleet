package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sessionMetaFor is the Meta behind fixture()'s sid.
func sessionMetaFor(sid, cwd string) session.Meta {
	return session.Meta{Name: "slot01", Dir: cwd, Kind: session.KindClaude}
}

// The usage fold's remembered miss must not leak into any reader that decides something
// (#3116): the same session, asked through the ordinary entry points right after the fold
// remembered its miss, still goes to the disk.
func TestFoldMissDoesNotLeakIntoBackgroundReaders(t *testing.T) {
	sid, cwd, subDir := fixture(t, 10)
	m := sessionMetaFor(sid, cwd)
	foldMisses = missMemo{ttlLive: foldMissTTLLive, ttlStopped: foldMissTTLArchived}
	(agentImpl{}).UsageMissNote(m)
	if !(agentImpl{}).UsageMissHeld(m) {
		t.Fatal("the miss was not remembered")
	}

	// A subagent transcript appears with no main transcript at all.
	mkdir(t, subDir)
	if err := os.WriteFile(filepath.Join(subDir, "agent-a.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := ProjectSweeps()
	if got := SubagentLogs(sid); len(got) != 1 {
		t.Fatalf("SubagentLogs = %v, want the new transcript", got)
	}
	if !SubagentBusy(sid) {
		t.Fatal("SubagentBusy missed a fresh subagent transcript while the fold held a miss")
	}
	if ProjectSweeps() == before {
		t.Fatal("the readers made no sweep: they are answering from a memo")
	}
}

func TestFoldMissExpiresAndFollowsArchive(t *testing.T) {
	sid, cwd, _ := fixture(t, 1)
	m := sessionMetaFor(sid, cwd)
	foldMisses = missMemo{ttlLive: foldMissTTLLive, ttlStopped: foldMissTTLArchived}
	key := foldMissKey(m)
	now := time.Now()
	foldMisses.note(key, true, now.Add(-foldMissTTLLive))
	if (agentImpl{}).UsageMissHeld(m) {
		t.Error("a live miss outlived its TTL")
	}
	m.Archived = true
	foldMisses.note(key, false, now.Add(-foldMissTTLLive-time.Second))
	if !(agentImpl{}).UsageMissHeld(m) {
		t.Error("an archived miss is not trusted for longer than a live one")
	}
	m.Archived = false
	if (agentImpl{}).UsageMissHeld(m) {
		t.Error("a miss seen while archived answered for the restored session")
	}
}
