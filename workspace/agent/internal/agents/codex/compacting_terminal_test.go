package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// A Terminal codex thread never reaches an app-server, so its compaction is known only from
// the injected PreCompact / PostCompact hooks (#1139). The state must open on PreCompact and
// close on PostCompact, on an Esc that fires neither PostCompact nor Stop (the rollout's
// turn_aborted, measured on 0.160.0), and on a relaunch of a pane killed mid-compaction.
func TestTerminalCompactingFollowsTheHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-compact", Dir: t.TempDir(), Kind: session.KindCodex}
	slot := session.UUID(m.Dir, m.Name)
	const thread = "01a10463-1ce3-7df3-aa43-c8900d974e88"
	now := time.Now()
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }
	fakeTmuxSession(t, now.Add(-time.Hour))
	status.Persist(slot, "idle")

	state := func() string { return (agentImpl{}).WireLive(m, true).State }
	chip := func() bool { td, _ := readTranscript(m); return td.Compacting }
	// The previous turn ended before the compaction began; that end must not close it.
	earlier := [][]byte{
		taskStarted(ts(-2*time.Minute), "t1"),
		userSays(ts(-2*time.Minute), "first"),
		taskComplete(ts(-time.Minute), "t1"),
	}
	writeSlotRollout(t, m, thread, earlier...)
	if got := state(); got != "idle" {
		t.Fatalf("before PreCompact: state = %q, want idle", got)
	}

	MarkCompacting(slot, true)
	if got := state(); got != "compacting" {
		t.Fatalf("after PreCompact: state = %q, want compacting", got)
	}
	if !chip() {
		t.Fatal("after PreCompact: transcript does not report compacting")
	}

	MarkCompacting(slot, false)
	if got := state(); got != "idle" {
		t.Fatalf("after PostCompact: state = %q, want idle", got)
	}

	// Esc during the compaction: no PostCompact, but the rollout ends the turn after the mark.
	MarkCompacting(slot, true)
	time.Sleep(2 * time.Millisecond)
	writeSlotRollout(t, m, thread, append(append([][]byte(nil), earlier...),
		taskStarted(ts(-30*time.Second), "t2"),
		turnAborted(time.Now().UTC().Format(time.RFC3339Nano), "t2"))...)
	if got := state(); got != "idle" {
		t.Fatalf("after an interrupted compaction: state = %q, want idle", got)
	}

	// A pane killed mid-compaction: the relaunch drops the mark.
	writeSlotRollout(t, m, thread, earlier...)
	MarkCompacting(slot, true)
	if got := state(); got != "compacting" {
		t.Fatalf("killed mid-compaction: state = %q, want compacting", got)
	}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	if got := state(); got != "idle" {
		t.Fatalf("after relaunch: state = %q, want idle", got)
	}
}

// A managed session's compaction comes from the driver's contextCompaction events; a stray
// hook mark for its slot must not make it report compacting a second time.
func TestManagedIgnoresTheTerminalCompactionMark(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-managed", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	slot := session.UUID(m.Dir, m.Name)
	writeSlotRollout(t, m, "01a10463-0000-7000-8000-000000000003")
	MarkCompacting(slot, true)
	if isCompacting(m) {
		t.Fatal("managed session reads the Terminal hook mark")
	}
}
