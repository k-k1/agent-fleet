package chatx

// A Stop that another Stop hook blocked must not settle the turn (#1600).
//
// Our Stop hook runs beside the user's and writes the idle marker before the blocking verdict
// exists (claude and codex both run a Stop event's hooks in parallel), so the marker of a
// blocked stop looks exactly like a finished turn. These tests drive the reconciler through
// "marker of the blocked stop → the continued turn stays quiet for more than two sweeps → the
// real end" and pin that the report waits for the real end and goes out once.
//
// The claude case stages the continued turn's last write three minutes back, past the
// transcript freshness window, and runs with no tmux pane: that is the state where none of
// the earlier busy evidence (transcript-busy, pane-busy) is left, i.e. a long thinking gap or
// a slow tool with the spinner unread. Compensation cannot repair an early report there
// either: evalReportResumed treats an idle marker as "the turn ended", and the continued
// turn's real end writes another idle marker.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// writeTurnEndMarkerAt writes the Stop hook's idle+TurnEnd record with a chosen time —
// status.PersistTurnEnd always stamps now, and the claude case needs a marker older than
// the transcript freshness window.
func writeTurnEndMarkerAt(t *testing.T, sid string, at time.Time) {
	t.Helper()
	ts := at.Format(time.RFC3339)
	b, _ := json.Marshal(status.SessionStatus{State: "idle", TS: ts, TurnEnd: true, TurnEndAt: ts, Rev: "t1600"})
	dir := filepath.Join(paths.AgentStateDir(), "session-status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestReportWaitsForTurnContinuedPastBlockedStopClaude(t *testing.T) {
	m, sid, convID := ledgerFixture(t, "slot1600c")
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	addInstructionAt(m.Name, convID, "operator", time.Now().Add(-10*time.Minute))
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	stop := time.Now().Add(-3 * time.Minute)
	ts := func(d time.Duration) string { return stop.Add(d).UTC().Format(time.RFC3339Nano) }
	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(proj, sid+".jsonl")
	// The shape claude 2.1.288 writes for a blocked Stop (measured), trimmed to the fields read.
	appendLines(t, transcript,
		`{"type":"user","timestamp":"`+ts(-7*time.Minute)+`","message":{"role":"user","content":"do the task"}}`,
		`{"type":"assistant","timestamp":"`+ts(-time.Second)+`","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"user","timestamp":"`+ts(time.Second)+`","isMeta":true,"message":{"role":"user","content":"Stop hook feedback:\nrun the tests first"}}`,
		`{"type":"attachment","timestamp":"`+ts(time.Second)+`","attachment":{"type":"hook_blocking_error","hookName":"Stop","hookEvent":"Stop"}}`,
		`{"type":"system","subtype":"stop_hook_summary","timestamp":"`+ts(time.Second)+`","hookCount":2,"hookErrors":["run the tests first"],"preventedContinuation":false}`,
		`{"type":"assistant","timestamp":"`+ts(3*time.Second)+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
	)
	writeTurnEndMarkerAt(t, sid, stop) // our hook's idle for the blocked stop

	for i := 0; i < 3; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if n := cs.count(); n != 0 {
		t.Fatalf("reported %d time(s) while the turn a Stop hook blocked was still running: %v", n, cs.callsSnapshot())
	}

	// The real end: the tool returns, claude answers, and an unblocked Stop writes a new marker.
	now := time.Now()
	nts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }
	appendLines(t, transcript,
		`{"type":"user","timestamp":"`+nts(-3*time.Second)+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"`+nts(-2*time.Second)+`","message":{"role":"assistant","content":[{"type":"text","text":"tests pass, done"}]}}`,
		`{"type":"system","subtype":"stop_hook_summary","timestamp":"`+nts(0)+`","hookCount":2,"hookErrors":[],"preventedContinuation":false}`,
	)
	writeTurnEndMarkerAt(t, sid, now)
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != ReportKindAnswerReady+":" {
		t.Fatalf("the real end must be reported once: %v", got)
	}
	if SessionReportPending(m.Name) {
		t.Fatal("the instruction is still open after the real end was reported")
	}
}

func TestReportWaitsForTurnContinuedPastBlockedStopCodex(t *testing.T) {
	m, sid, convID := ledgerFixture(t, "slot1600x")
	m.Kind = session.KindCodex
	session.WriteMeta(m)
	addInstructionAt(m.Name, convID, "operator", time.Now().Add(-10*time.Minute))
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	const thread = "01a0ee31-0000-7000-8000-000000001600"
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "10", "04")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-10-04T00-00-00-"+thread+".jsonl")
	codex.RememberSid(sid, thread)
	now := time.Now()
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }
	// On a block codex records the hook's prompt and continues the same turn: no task_complete.
	appendLines(t, rollout,
		`{"timestamp":"`+ts(-time.Minute)+`","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"`+ts(-time.Minute)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do the task"}]}}`,
		`{"timestamp":"`+ts(-2*time.Second)+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		`{"timestamp":"`+ts(-time.Second)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run the tests first"}]}}`,
	)
	status.PersistTurnEnd(sid, "idle") // our hook's idle for the blocked stop

	for i := 0; i < 3; i++ {
		clock.advance(t, rc, reportTickDefault)
	}
	if n := cs.count(); n != 0 {
		t.Fatalf("reported %d time(s) while the turn a Stop hook blocked was still running: %v", n, cs.callsSnapshot())
	}

	appendLines(t, rollout,
		`{"timestamp":"`+ts(time.Second)+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"tests pass, done"}]}}`,
		`{"timestamp":"`+ts(2*time.Second)+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
	)
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != ReportKindAnswerReady+":" {
		t.Fatalf("the real end must be reported once: %v", got)
	}
}

// The continued turn can also end on an API error, which fires no Stop: the abort at the
// tail has to win over the blocked stop, or that instruction would wait for the next prompt.
func TestReportBlockedStopThenAbortIsReported(t *testing.T) {
	m, sid, convID := ledgerFixture(t, "slot1600a")
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false,"claudeAbortAutoResume":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	addInstructionAt(m.Name, convID, "operator", time.Now().Add(-10*time.Minute))
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	stop := time.Now().Add(-3 * time.Minute)
	ts := func(d time.Duration) string { return stop.Add(d).UTC().Format(time.RFC3339Nano) }
	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	appendLines(t, filepath.Join(proj, sid+".jsonl"),
		`{"type":"assistant","timestamp":"`+ts(-time.Second)+`","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"attachment","timestamp":"`+ts(time.Second)+`","attachment":{"type":"hook_blocking_error","hookEvent":"Stop"}}`,
		`{"type":"system","subtype":"stop_hook_summary","timestamp":"`+ts(time.Second)+`"}`,
		`{"type":"assistant","timestamp":"`+ts(5*time.Second)+`","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}]}}`,
	)
	writeTurnEndMarkerAt(t, sid, stop)

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != ReportKindAnswerReady+":"+ReportReasonTurnAborted {
		t.Fatalf("the continued turn's abort must be reported: %v", got)
	}
}
