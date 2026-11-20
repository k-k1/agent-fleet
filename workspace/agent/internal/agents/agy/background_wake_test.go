package agy

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The finished-turn mark moves only when an executor run ends: a background completion notice
// agy writes without running the model leaves it where it was, and a turn agy starts on its own
// moves it once that turn ends. Step sequences as in TestLiveStateFollowsTurnEndNotLastStepStatus.
func TestFinishedTurnMarkMovesOnlyWithATurnEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	const (
		user, model, tool, notice = 14, 15, 132, 101
		completed                 = 4
	)
	done := func(types ...int) [][3]any {
		var out [][3]any
		for _, ty := range types {
			out = append(out, [3]any{ty, stepStatusDone, []byte("x")})
		}
		return out
	}
	markOf := func(name string, rows [][3]any, turns [][]byte) (string, bool) {
		slot := "slot-mark-" + name
		conv := "conv-mark-" + name
		m := session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy}
		sids.Write(session.UUID(dir, slot), conv)
		mkConvDBWithTurns(t, conv, rows, turns)
		return agentImpl{}.FinishedTurnMark(m)
	}

	interim, ok := markOf("interim", done(user, model, tool, model), [][]byte{executorRow(completed, 3)})
	if !ok || interim != "conv-mark-interim:3" {
		t.Fatalf("interim end: got %q ok=%v, want conv-mark-interim:3", interim, ok)
	}
	if got, ok := markOf("notice", done(user, model, tool, model, notice), [][]byte{executorRow(completed, 3)}); !ok || got != "conv-mark-notice:3" {
		t.Fatalf("a completion notice alone moved the mark: %q ok=%v", got, ok)
	}
	if got, ok := markOf("resumed", done(user, model, tool, model, notice, model),
		[][]byte{executorRow(completed, 3), executorRow(completed, 5)}); !ok || got != "conv-mark-resumed:5" {
		t.Fatalf("a self-started turn's end did not move the mark: %q ok=%v", got, ok)
	}
	if got, ok := markOf("noturn", done(user), nil); ok {
		t.Fatalf("no finished turn yet must have no mark, got %q", got)
	}
}
