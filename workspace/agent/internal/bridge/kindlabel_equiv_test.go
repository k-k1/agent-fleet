package bridge

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
)

// productLabel replaces kindLabel's kind switch (#2870): until the switch is deleted, the two
// must name every registered kind, and every string that is none, the same way.
func TestProductLabelEqualsKindLabel(t *testing.T) {
	named := 0
	for _, k := range append(kinds.All(), "", "Claude", " codex", "llama.cpp", "pi", "bash") {
		if got, want := productLabel(k), kindLabel(k); got != want {
			t.Errorf("productLabel(%q) = %q, kindLabel = %q", k, got, want)
		}
		if kindLabel(k) != k {
			named++
		}
	}
	// The switch has 8 arms; opencode's label is its own id, so 7 differ from the kind.
	if named != 7 {
		t.Fatalf("kindLabel renamed %d kinds, want 7; the comparison is not reading the switch", named)
	}
}
