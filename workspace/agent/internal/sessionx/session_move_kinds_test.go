package sessionx

// moveTargetKinds is a measured fact per kind (ADR 0115 decision 9), not a capability, so it
// stays a hand-kept list; this table is what makes a new kind choose.

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveRefusals are the registered kinds that are deliberately not move targets, and the reason
// each is refused. Every other registered kind must be on moveTargetKinds (answer "") or on
// moveUnmeasuredTargets (answer moveTargetNotMeasured), which are where the measurement lives, so
// adding a measured kind edits only those lists. A kind on neither list and not here is a kind
// nobody has classified, and fails TestEveryKindIsClassifiedForMove.
var moveRefusals = map[string]string{
	session.KindLcpp:  moveTargetCannotRead,
	session.KindShell: moveTargetNotAnAgent,
	session.KindSSM:   moveTargetNotAnAgent,
}

func TestEveryKindIsClassifiedForMove(t *testing.T) {
	for _, k := range RegisteredKinds() {
		want, refused := moveRefusals[k]
		switch {
		case slices.Contains(moveTargetKinds, k):
			want = ""
		case slices.Contains(moveUnmeasuredTargets, k):
			want = moveTargetNotMeasured
		case !refused:
			t.Errorf("kind %q is registered but not classified as a move target: measure it and add it to moveTargetKinds or moveUnmeasuredTargets, or refuse it with a reason in moveRefusals", k)
			continue
		}
		if refused && (slices.Contains(moveTargetKinds, k) || slices.Contains(moveUnmeasuredTargets, k)) {
			t.Errorf("kind %q is both refused and on a target list", k)
		}
		if got := moveTargetReason("other", k); got != want {
			t.Errorf("moveTargetReason(other, %q) = %q, want %q", k, got, want)
		}
	}
	for k := range moveRefusals {
		if _, ok := RegisteredAgent(k); !ok {
			t.Errorf("moveRefusals has %q, which is not a registered kind", k)
		}
	}
	for _, k := range append(slices.Clone(moveTargetKinds), moveUnmeasuredTargets...) {
		if _, ok := RegisteredAgent(k); !ok {
			t.Errorf("a move target list has %q, which is not a registered kind", k)
		}
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
