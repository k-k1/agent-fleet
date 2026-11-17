package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// tsLine is a transcript record with a timestamp, the way claude writes one.
func tsLine(typ string, at time.Time, content any) string {
	b, _ := json.Marshal(map[string]any{
		"type": typ, "timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"message": map[string]any{"role": typ, "content": content},
	})
	return string(b)
}

// queueSession plants the transcript of a Terminal claude session and starts the delivery's first
// turn the way the hooks do: armed at create, delivering, then UserPromptSubmit's working write.
func queueSession(t *testing.T, lines ...string) string {
	t.Helper()
	return queueSessionWith(t, func() []string { return lines })
}

// queueSessionWith is queueSession for a transcript that has to be stamped after the first turn
// has started: SettleFirstTurnInterrupted ignores an interruption older than the turn's start, so
// a timestamp taken before the start (the arguments of a call are evaluated first) loses to any
// stall between the two.
func queueSessionWith(t *testing.T, lines func() []string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	const sid = "21111111-2222-5333-8444-555555555555"
	dir := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "q_d1")
	status.PersistOpen(sid, "working", "p1")
	body := ""
	for _, l := range lines() {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return sid
}

// Esc on a running claude turn fires no Stop hook (stop_blocked.go): the record stays working
// until the pane is seen back at its prompt, and the heal then deletes it. The interruption is
// in the transcript, so the first turn is reported as aborted instead of being voided.
func TestInterruptedFirstTurnOfATerminalSessionIsAborted(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	for name, text := range map[string]string{
		"while it streams": "[Request interrupted by user]",
		"during a tool":    "[Request interrupted by user for tool use]",
	} {
		t.Run(name, func(t *testing.T) {
			sid := queueSessionWith(t, func() []string {
				return []string{
					tsLine("user", start, "the queue's task"),
					tsLine("assistant", start.Add(5*time.Second), []any{map[string]any{"type": "text", "text": "working"}}),
					tsLine("user", time.Now().Add(time.Second), []any{map[string]any{"type": "text", "text": text}}),
				}
			})
			HealIdle(sid)
			got, ok := status.ReadFirstTurn(sid)
			if !ok || got.Outcome != status.TurnEndReasonAborted {
				t.Fatalf("first turn = %+v ok=%v, want turn-aborted", got, ok)
			}
		})
	}
}

// The newest record is an interruption, but of an EARLIER turn: the first turn's own prompt is not
// in the transcript yet. It must not be reported as the first turn's end.
func TestAnEarlierInterruptionIsNotTheFirstTurnsEnd(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	sid := queueSession(t,
		tsLine("user", old, []any{map[string]any{"type": "text", "text": "[Request interrupted by user]"}}),
	)
	HealIdle(sid)
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("an earlier turn's interruption was reported as the first turn's: %+v", got)
	}
}

// A heal that finds an ordinary end (no interruption, no error) stays silent and the ledger stays
// empty: nothing proves how the turn ended.
func TestAHealWithoutAnInterruptionLeavesTheFirstTurnAbsent(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	sid := queueSession(t,
		tsLine("user", start, "the queue's task"),
		tsLine("assistant", start.Add(5*time.Second), []any{map[string]any{"type": "text", "text": "done"}}),
	)
	HealIdle(sid)
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("a heal with no evidence settled the first turn: %+v", got)
	}
}

// A first turn that really fails on a Terminal session: the API error is the newest record and the
// heal carries it through the notifier, which writes the reason with the end.
func TestFailedFirstTurnOfATerminalSessionIsReported(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	sid := queueSession(t,
		tsLine("user", start, "the queue's task"),
		apiErr("You've reached your Fable 5 limit. Run /usage-credits to continue", 429),
	)
	HealIdle(sid)
	got, ok := status.ReadFirstTurn(sid)
	if !ok || got.Outcome != status.TurnEndReasonFailed {
		t.Fatalf("first turn = %+v ok=%v, want turn-failed", got, ok)
	}
}
