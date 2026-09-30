package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Esc on a running turn writes turn_aborted to the rollout and fires no Stop hook (measured
// 0.159.0, #1264), so the status store keeps "working". The badge has to read the end off the
// rollout — but only an end that belongs to the turn the store says is running.
func TestMissedTurnEndCountsTurnAborted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_TMUX_SOCKET", "af-test-missed-turn-end-none")
	m := session.Meta{Name: "cx-esc", Dir: t.TempDir(), Kind: session.KindCodex}
	sid := session.UUID(m.Dir, m.Name)
	const thread = "01a0ee31-0000-7000-8000-000000000002"
	ts := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339Nano) }

	// The previous turn was interrupted a minute ago; a new prompt has just moved the store to
	// working, and codex has not written that turn's task_started yet.
	earlier := [][]byte{
		taskStarted(ts(-2*time.Minute), "t1"),
		userSays(ts(-2*time.Minute), "first"),
		turnAborted(ts(-time.Minute), "t1"),
	}
	writeSlotRollout(t, m, thread, earlier...)
	status.Persist(sid, "working")
	if MissedTurnEnd(m) {
		t.Fatal("an earlier turn's turn_aborted ended the newer running turn")
	}

	running := append(earlier, taskStarted(ts(time.Second), "t2"), userSays(ts(time.Second), "second"))
	writeSlotRollout(t, m, thread, running...)
	if MissedTurnEnd(m) {
		t.Fatal("a running turn read as ended")
	}
	if got := (agentImpl{}).WireLive(m, true).State; got != "working" {
		t.Fatalf("running turn: WireLive = %q, want working", got)
	}

	// Esc on a question: codex writes the call's "aborted by user" output, then turn_aborted.
	aborted := append(running,
		askUser(ts(2*time.Second), "call_q", "Which fruit?"),
		callOutput(ts(3*time.Second), "call_q", "aborted by user after 1.0s"),
		turnAborted(ts(3*time.Second), "t2"))
	writeSlotRollout(t, m, thread, aborted...)
	if !MissedTurnEnd(m) {
		t.Fatal("turn_aborted of the running turn did not end it")
	}
	if got := (agentImpl{}).WireLive(m, true).State; got != "idle" {
		t.Fatalf("after Esc: WireLive = %q, want idle", got)
	}
	if got := TerminalModal(m); got != "" {
		t.Fatalf("after Esc: TerminalModal = %q, want none (the question closed with its turn)", got)
	}
	// The heal only answers: the stored state stays for the next hook to replace.
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("status store = %q, want it left at working", st.State)
	}

	// The same end still counts through task_complete.
	writeSlotRollout(t, m, thread, append(running, taskComplete(ts(4*time.Second), "t2"))...)
	if !MissedTurnEnd(m) {
		t.Fatal("task_complete of the running turn did not end it")
	}
}
