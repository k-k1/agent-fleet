package agentreg

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// internal/kinds is what the packages below this one consult; a kind registered here and
// missing there (or the reverse) is a kind those packages silently do not know.
func TestRegistryMatchesTheKindsList(t *testing.T) {
	if got, want := Kinds(), slices.Sorted(slices.Values(kinds.All())); !slices.Equal(got, want) {
		t.Fatalf("agentreg kinds = %v, internal/kinds = %v", got, want)
	}
	for _, k := range kinds.All() {
		if _, ok := Lookup(k); !ok {
			t.Errorf("Lookup(%q) = false for a kind internal/kinds lists", k)
		}
	}
}

func TestOfFallsBackToClaudeAndLookupDoesNot(t *testing.T) {
	for _, k := range []string{"", "Claude", " codex", "kiro-cli", "pi"} {
		if got := Of(k).Kind(); got != session.KindClaude {
			t.Errorf("Of(%q).Kind() = %q, want claude", k, got)
		}
		if _, ok := Lookup(k); ok {
			t.Errorf("Lookup(%q) = true", k)
		}
	}
}

type fakeAgent struct{ agents.Agent }

func TestSwapForTestRestores(t *testing.T) {
	prev, _ := Lookup(session.KindCodex)
	restore := SwapForTest(session.KindCodex, fakeAgent{prev})
	if a, _ := Lookup(session.KindCodex); a == prev {
		t.Fatal("SwapForTest did not register the stand-in")
	}
	restore()
	if a, _ := Lookup(session.KindCodex); a != prev {
		t.Fatal("restore did not put the previous agent back")
	}

	restore = SwapForTest("pi", fakeAgent{prev})
	if !slices.Contains(Kinds(), "pi") {
		t.Fatal("a swapped-in new kind is not registered")
	}
	restore()
	if _, ok := Lookup("pi"); ok || slices.Contains(Kinds(), "pi") {
		t.Fatal("restore left a new kind registered")
	}
}
