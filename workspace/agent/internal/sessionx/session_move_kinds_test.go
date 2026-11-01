package sessionx

// A move target is a measured fact per kind (ADR 0115 decision 9), not a capability, so each kind
// declares it (agents.MoveTarget); this table is what makes a new kind choose.

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveRefusals are the registered kinds that are deliberately not move targets, and the reason
// each is refused. Every other registered kind must be on declare agents.MoveTarget (answer "" or
// moveTargetNotMeasured), which is where the measurement lives, so adding a measured kind edits
// only its own package. A kind on no declaration and not here is a kind
// nobody has classified, and fails TestEveryKindIsClassifiedForMove.
var moveRefusals = map[string]string{
	session.KindLcpp:  moveTargetCannotRead,
	session.KindShell: moveTargetNotAnAgent,
	session.KindSSM:   moveTargetNotAnAgent,
}

// Measurements already shipped (ADR 0115 decision 9): these kinds are move targets, cursor is
// offered as unmeasured, and the refusals above stand. A new kind needs no row here; moving one of
// these between lists is a measurement change and edits this snapshot on purpose.
var (
	shippedMoveTargets    = []string{"claude", "codex", "opencode", "copilot", "agy", "muse", "kiro"}
	shippedMoveUnmeasured = []string{"cursor"}
)

func TestShippedMoveClassificationHolds(t *testing.T) {
	for _, k := range shippedMoveTargets {
		if !slices.Contains(moveTargetKinds(), k) {
			t.Errorf("%q was a measured move target and is not on moveTargetKinds any more", k)
		}
	}
	for _, k := range shippedMoveUnmeasured {
		if !slices.Contains(moveUnmeasuredTargets(), k) {
			t.Errorf("%q was an unmeasured move target and is not on moveUnmeasuredTargets any more", k)
		}
	}
	for k, want := range moveRefusals {
		if got := moveTargetReason("other", k); got != want {
			t.Errorf("moveTargetReason(other, %q) = %q, want %q", k, got, want)
		}
	}
}

// The lists are disjoint, duplicate-free and hold only registered kinds.
func TestMoveListsAreWellFormed(t *testing.T) {
	seen := map[string]string{}
	for list, kinds := range map[string][]string{"moveTargetKinds": moveTargetKinds(), "moveUnmeasuredTargets": moveUnmeasuredTargets()} {
		for _, k := range kinds {
			if prev, dup := seen[k]; dup {
				t.Errorf("%q is on %s and %s", k, prev, list)
			}
			seen[k] = list
		}
	}
	for _, list := range [][]string{moveTargetKinds(), moveUnmeasuredTargets()} {
		if len(slices.Compact(slices.Sorted(slices.Values(list)))) != len(list) {
			t.Errorf("a move target list has a duplicate: %v", list)
		}
	}
}

func TestEveryKindIsClassifiedForMove(t *testing.T) {
	for _, k := range RegisteredKinds() {
		want, refused := moveRefusals[k]
		switch {
		case slices.Contains(moveTargetKinds(), k):
			want = ""
		case slices.Contains(moveUnmeasuredTargets(), k):
			want = moveTargetNotMeasured
		case !refused:
			t.Errorf("kind %q is registered but not classified as a move target: measure it and declare agents.MoveTarget in its package, or refuse it with a reason in moveRefusals", k)
			continue
		}
		if refused && (slices.Contains(moveTargetKinds(), k) || slices.Contains(moveUnmeasuredTargets(), k)) {
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
	for _, k := range append(moveTargetKinds(), moveUnmeasuredTargets()...) {
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

// oldMoveTargetKinds and oldMoveUnmeasuredTargets are literal copies of the hand-kept lists the
// declarations replaced (#2933), so moving them into the kind packages is held to byte-identical
// answers. Deliberately not the shipped* snapshot above, which is the independent pin.
var (
	oldMoveTargetKinds       = []string{"claude", "codex", "opencode", "copilot", "agy", "muse", "kiro"}
	oldMoveUnmeasuredTargets = []string{"cursor"}
)

func oldMoveTargetReason(source, target string) string {
	switch {
	case target == source:
		return "same_kind"
	case slices.Contains(oldMoveTargetKinds, target):
		return ""
	case slices.Contains(oldMoveUnmeasuredTargets, target):
		return "not_measured"
	case target == "lcpp":
		return "cannot_read_file"
	}
	return "not_an_agent"
}

func TestMoveTargetDeclarationsMatchOldLists(t *testing.T) {
	kinds := append(RegisteredKinds(), "", "Claude", "claude-code", "codex-cli", "antigravity",
		"kiro-cli", "cursor-agent", "github-copilot", "llamacpp", "llama.cpp", "unknown", " cursor")
	for _, src := range kinds {
		for _, tgt := range kinds {
			if got, want := moveTargetReason(src, tgt), oldMoveTargetReason(src, tgt); got != want {
				t.Errorf("moveTargetReason(%q, %q) = %q, old lists said %q", src, tgt, got, want)
			}
		}
	}
	if got := slices.Sorted(slices.Values(moveTargetKinds())); !slices.Equal(got, slices.Sorted(slices.Values(oldMoveTargetKinds))) {
		t.Errorf("moveTargetKinds() = %v", got)
	}
	if got := moveUnmeasuredTargets(); !slices.Equal(got, oldMoveUnmeasuredTargets) {
		t.Errorf("moveUnmeasuredTargets() = %v", got)
	}
}

// A declaration must be one of the two defined states: zero or an out-of-range value would make
// the kind silently "not a target", and a kind cannot be both by construction of one method.
func TestMoveTargetDeclarationIsAKnownState(t *testing.T) {
	for _, k := range RegisteredKinds() {
		a, _ := RegisteredAgent(k)
		m, ok := a.(agents.MoveTarget)
		if !ok {
			continue
		}
		if st := m.MoveTarget(); st != agents.MoveTargetMeasured && st != agents.MoveTargetUnmeasured {
			t.Errorf("%s declares MoveTarget %d, which is neither measured nor unmeasured", k, st)
		}
		if _, refused := moveRefusals[k]; refused {
			t.Errorf("%s both declares MoveTarget and is on moveRefusals", k)
		}
	}
}

func TestAnyEligibleTargetIsEligible(t *testing.T) {
	for _, src := range RegisteredKinds() {
		if got := anyEligibleTarget(src); got == src || moveTargetReason(src, got) != "" {
			t.Errorf("anyEligibleTarget(%q) = %q", src, got)
		}
	}
}
