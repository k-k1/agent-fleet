package sessionx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// The chat chip reads DriveState, the sessions-list badge reads WireLive. After an Esc on a
// codex Terminal turn (turn_aborted in the rollout, no Stop hook) both must stop saying "in
// progress" (#1264).
func TestDriveStateCodexTurnAborted(t *testing.T) {
	isolateAgentState(t)
	m := session.Meta{Name: "cxesc1", Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	const thread = "01a0ee31-0000-7000-8000-000000000003"
	codex.RememberSid(sid, thread)
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "09", "30")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-09-30T02-23-46-"+thread+".jsonl")
	ts := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339Nano) }
	started := `{"timestamp":"` + ts(time.Second) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n"
	aborted := `{"timestamp":"` + ts(2*time.Second) + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}` + "\n"

	status.Persist(sid, "working")
	if err := os.WriteFile(rollout, []byte(started), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DriveState(m, true, true); got != "working" {
		t.Fatalf("running turn: DriveState = %q, want working", got)
	}
	if err := os.WriteFile(rollout, []byte(started+aborted), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DriveState(m, true, true); got != "idle" {
		t.Fatalf("after Esc: DriveState = %q, want idle", got)
	}
	if got := DriveState(m, true, false); got != "idle" {
		t.Fatalf("after Esc, heal=false: DriveState = %q, want idle (the badge does not depend on heal either)", got)
	}
}
