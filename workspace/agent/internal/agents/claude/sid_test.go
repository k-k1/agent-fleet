package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateSlot points ConfigDir() (where the jsonl lives), AgentConfigDir() (the claude-sid
// ledger) and MetaDir() at temp dirs, so the real fleet's claude config and sessions are
// never touched — a test has written into the real .claude.json before.
func isolateSlot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("AF_SESSION_NAME", "")
	os.Unsetenv("AGENT_SESSION_CMD")
	return cfg
}

// writeSlotJSONL materializes a conversation log for id, the way claude would.
func writeSlotJSONL(t *testing.T, cfg, project, id string) string {
	t.Helper()
	dir := filepath.Join(cfg, "projects", project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	testSlotSID = "b7000000-0000-5000-8000-00000000slot"
	testLiveSID = "47000000-0000-4000-8000-00000000live"
)

// With no ledger the slot sid is used as is — the ordinary, undrifted session.
func TestLiveSIDWithoutLedgerIsSlot(t *testing.T) {
	isolateSlot(t)
	if got := LiveSID(testSlotSID); got != testSlotSID {
		t.Fatalf("LiveSID = %q, want the slot sid %q", got, testSlotSID)
	}
}

// After a drift the deterministic sid's jsonl never appears, so the real log the ledger
// points at is read instead. Without this the mirror stays stuck on "no conversation yet".
func TestJSONLPathsFollowsDriftedSID(t *testing.T) {
	cfg := isolateSlot(t)
	want := writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)
	sids.Write(testSlotSID, testLiveSID)

	if got := LiveSID(testSlotSID); got != testLiveSID {
		t.Fatalf("LiveSID = %q, want the drifted id %q", got, testLiveSID)
	}
	got := jsonlPaths(testSlotSID)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("jsonlPaths = %v, want [%s]", got, want)
	}
	if !SessionJSONLExists(testSlotSID) {
		t.Fatal("SessionJSONLExists = false — a real log exists but the session is treated as new")
	}
}

// When what the ledger points at is gone, fall back to the slot silently. A stale entry must
// not distort the "there is no conversation" verdict.
func TestLiveSIDIgnoresStaleLedger(t *testing.T) {
	isolateSlot(t)
	sids.Write(testSlotSID, testLiveSID) // no matching jsonl is written

	if got := LiveSID(testSlotSID); got != testSlotSID {
		t.Fatalf("LiveSID = %q, want the slot sid %q when the ledger dangles", got, testSlotSID)
	}
	if SessionJSONLExists(testSlotSID) {
		t.Fatal("SessionJSONLExists = true — a dangling ledger entry is being read as a conversation")
	}
}

// A restart must resume the id claude is actually writing. Passing the slot sid fails with
// "No conversation found", and the conversation silently disappears on every restart.
func TestBuildProgramResumesDriftedSID(t *testing.T) {
	cfg := isolateSlot(t)
	writeSlotJSONL(t, cfg, "-tmp-repo", testLiveSID)
	sids.Write(testSlotSID, testLiveSID)

	got := buildProgram(testSlotSID, "", "", "", "", "", true)
	if !strings.Contains(got, "--resume '"+testLiveSID+"'") {
		t.Fatalf("program = %q, want --resume of the drifted id %q", got, testLiveSID)
	}
	if strings.Contains(got, testSlotSID) {
		t.Fatalf("program = %q, must not carry the slot sid claude no longer knows", got)
	}
}

// When the id a hook reports differs from ours, it is mapped back to the slot and the
// correspondence recorded. Without the record, the next poll and the next restart both keep
// looking at the deterministic sid and find nothing.
func TestNormalizeHookSIDRecordsDrift(t *testing.T) {
	isolateSlot(t)
	m := session.Meta{Name: "s56ynzz", Dir: "/tmp/repo", Kind: session.KindClaude}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)

	if got := NormalizeHookSID(testLiveSID); got != slot {
		t.Fatalf("NormalizeHookSID = %q, want the slot sid %q", got, slot)
	}
	if got := sids.Read(slot); got != testLiveSID {
		t.Fatalf("ledger = %q, want %q", got, testLiveSID)
	}
}

// Once the drift is healed (relaunched with --session-id taking effect), the ledger entry is
// folded away. Left in place, it would try to resume a conversation that is gone.
func TestNormalizeHookSIDClearsHealedDrift(t *testing.T) {
	isolateSlot(t)
	m := session.Meta{Name: "s56ynzz", Dir: "/tmp/repo", Kind: session.KindClaude}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)
	sids.Write(slot, testLiveSID)

	if got := NormalizeHookSID(slot); got != slot {
		t.Fatalf("NormalizeHookSID = %q, want %q", got, slot)
	}
	if got := sids.Read(slot); got != "" {
		t.Fatalf("ledger = %q, want it cleared", got)
	}
}

// Hooks from a claude outside AF's management (one the user started themselves) pass
// through. Never tie them to someone else's session on a guess such as a matching cwd.
func TestNormalizeHookSIDPassesThroughUnmanaged(t *testing.T) {
	isolateSlot(t)
	if got := NormalizeHookSID(testLiveSID); got != testLiveSID {
		t.Fatalf("NormalizeHookSID = %q, want it untouched without AF_SESSION_NAME", got)
	}
}
