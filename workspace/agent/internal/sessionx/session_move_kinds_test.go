package sessionx

// moveTargetKinds is a measured fact per kind (ADR 0115 decision 9), not a capability, so it
// stays a hand-kept list; this table is what makes a new kind choose.

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveClassification is every registered kind's answer as a TARGET of a move from a kind
// that is not itself. A kind registered without a row here fails TestEveryKindIsClassifiedForMove.
var moveClassification = map[string]string{
	session.KindClaude:   "",
	session.KindCodex:    "",
	session.KindOpencode: "",
	session.KindCopilot:  "",
	session.KindAgy:      "",
	session.KindMuse:     "",
	session.KindKiro:     "",
	session.KindCursor:   moveTargetNotMeasured,
	session.KindLcpp:     moveTargetCannotRead,
	session.KindShell:    moveTargetNotAnAgent,
	session.KindSSM:      moveTargetNotAnAgent,
}

func TestEveryKindIsClassifiedForMove(t *testing.T) {
	for _, k := range RegisteredKinds() {
		want, ok := moveClassification[k]
		if !ok {
			t.Errorf("kind %q is registered but not classified as a move target: measure it and add it to moveTargetKinds, moveUnmeasuredTargets or the refusals in moveTargetReason, then add a row to moveClassification", k)
			continue
		}
		if got := moveTargetReason("other", k); got != want {
			t.Errorf("moveTargetReason(other, %q) = %q, want %q", k, got, want)
		}
	}
	for k := range moveClassification {
		if _, ok := agentRegistry[k]; !ok {
			t.Errorf("moveClassification has %q, which is not a registered kind", k)
		}
	}
}

// The pre-change answer, as literals: the two lists a kind can be on, in order.
func TestMoveTargetListsAreUnchanged(t *testing.T) {
	wantTargets := []string{"claude", "codex", "opencode", "copilot", "agy", "muse", "kiro"}
	if !slices.Equal(moveTargetKinds, wantTargets) {
		t.Errorf("moveTargetKinds = %v, want %v", moveTargetKinds, wantTargets)
	}
	if !slices.Equal(moveUnmeasuredTargets, []string{"cursor"}) {
		t.Errorf("moveUnmeasuredTargets = %v, want [cursor]", moveUnmeasuredTargets)
	}
}

// Case and spelling variants are not kinds: an alias must never become a move target.
func TestMoveTargetRefusesAlternateSpellings(t *testing.T) {
	for _, k := range []string{"", "Claude", "CLAUDE", " claude", "claude ", "claude-code", "codex-cli",
		"antigravity", "kiro-cli", "cursor-agent", "github-copilot", "Muse", "llamacpp", "llama.cpp"} {
		if got := moveTargetReason("other", k); got != moveTargetNotAnAgent {
			t.Errorf("moveTargetReason(other, %q) = %q, want %q", k, got, moveTargetNotAnAgent)
		}
	}
	// A target spelled like the source is not the same kind.
	if got := moveTargetReason("claude", "Claude"); got != moveTargetNotAnAgent {
		t.Errorf("moveTargetReason(claude, Claude) = %q, want %q", got, moveTargetNotAnAgent)
	}
	if got := moveTargetReason("claude", "claude"); got != moveTargetSameKind {
		t.Errorf("moveTargetReason(claude, claude) = %q, want %q", got, moveTargetSameKind)
	}
}
