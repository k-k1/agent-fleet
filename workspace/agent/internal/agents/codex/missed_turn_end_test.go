package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Esc on a running turn writes turn_aborted to the rollout and fires no Stop hook (measured
// 0.159.0, #1264), so the status store keeps "working". The badge has to read the end off the
// rollout — but only an end that belongs to the turn the store says is running, and never while
// a question of that turn is still on the pane.
func TestMissedTurnEndCountsTurnAborted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-esc", Dir: t.TempDir(), Kind: session.KindCodex}
	sid := session.UUID(m.Dir, m.Name)
	const thread = "01a0ee31-0000-7000-8000-000000000002"
	now := time.Now()
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }
	// A pane older than every question below, so an open one is on screen: without it
	// PendingQuestionID answers "" and a question left open would go unnoticed here.
	fakeTmuxSession(t, now.Add(-time.Hour))
	with := func(base [][]byte, more ...[]byte) [][]byte {
		return append(append([][]byte(nil), base...), more...)
	}
	check := func(what string, lines [][]byte, ended bool, live, modal string) {
		t.Helper()
		writeSlotRollout(t, m, thread, lines...)
		if got := MissedTurnEnd(m); got != ended {
			t.Errorf("%s: MissedTurnEnd = %v, want %v", what, got, ended)
		}
		if got := (agentImpl{}).WireLive(m, true).State; got != live {
			t.Errorf("%s: WireLive = %q, want %q", what, got, live)
		}
		if got := TerminalModal(m); got != modal {
			t.Errorf("%s: TerminalModal = %q, want %q", what, got, modal)
		}
		// The chat's question card reads the parse's own closure, not PendingQuestionID's.
		if td, _ := readTranscript(m); (len(td.Pending) > 0) != (modal == "question") {
			t.Errorf("%s: question card = %+v, want open=%v", what, td.Pending, modal == "question")
		}
	}

	// The previous turn was interrupted a minute ago; a new prompt has just moved the store to
	// working, and codex has not written that turn's task_started yet.
	earlier := [][]byte{
		taskStarted(ts(-2*time.Minute), "t1"),
		userSays(ts(-2*time.Minute), "first"),
		turnAborted(ts(-time.Minute), "t1"),
	}
	status.Persist(sid, "working")
	check("earlier turn's abort", earlier, false, "working", "")

	running := with(earlier, taskStarted(ts(time.Second), "t2"), userSays(ts(time.Second), "second"))
	check("running turn", running, false, "working", "")

	asking := with(running, askUser(ts(2*time.Second), "call_q", "Which fruit?"))
	check("question open", asking, false, "question", "question")

	// Esc on the question: codex writes the call's "aborted by user" output, then turn_aborted.
	check("Esc on the question", with(asking,
		callOutput(ts(3*time.Second), "call_q", "aborted by user after 1.0s"),
		turnAborted(ts(3*time.Second), "t2")), true, "idle", "")
	// turn_aborted alone has to close the question too, not only the output line.
	check("turn_aborted with no output", with(asking, turnAborted(ts(3*time.Second), "t2")), true, "idle", "")

	// The order decides, not the clock alone: a turn started after the abort is running again.
	check("new turn after the abort", with(asking,
		turnAborted(ts(3*time.Second), "t2"),
		taskStarted(ts(4*time.Second), "t3")), false, "working", "")

	// The same end still counts through task_complete.
	check("task_complete", with(running, taskComplete(ts(4*time.Second), "t2")), true, "idle", "")

	// The heal only answers: the stored state stays for the next hook to replace.
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("status store = %q, want it left at working", st.State)
	}
}
