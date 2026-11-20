package chatx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// busyAgent wraps a registered agent and answers the stop-arm background probe with a fixed
// verdict, so the test sees which kind the dispatch reached and not the process tree.
type busyAgent struct {
	agents.Agent
	busy bool
}

func (b busyAgent) StopBackgroundBusy(session.Meta) bool { return b.busy }

// TestStopArmBackgroundBusyDispatch pins the lookup: an unknown or empty kind reads as claude
// (as the old switch's normalizeKind did), and a kind without the capability is never busy. The test's normalizeKind folds kinds that are
// not chat providers to claude, so those are skipped.
func TestStopArmBackgroundBusyDispatch(t *testing.T) {
	claudeAgent, ok := agentreg.Lookup(session.KindClaude)
	if !ok {
		t.Fatal("claude is not registered")
	}
	restore := agentreg.SwapForTest(session.KindClaude, busyAgent{claudeAgent, true})
	defer restore()

	for _, k := range []string{session.KindClaude, "", "unknown-kind"} {
		if !stopArmBackgroundBusy(session.Meta{Name: "s", Kind: k}) {
			t.Errorf("kind %q should reach claude's probe", k)
		}
	}
	for _, k := range agentreg.Kinds() {
		a, _ := agentreg.Lookup(k)
		if _, has := a.(agents.StopBackgroundBusyer); has || normalizeKind(k) != k {
			continue
		}
		if stopArmBackgroundBusy(session.Meta{Name: "s", Kind: k}) {
			t.Errorf("kind %q has no probe and must not be busy", k)
		}
	}
}
