package claude

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// The empty-anchor refusal is the shared bilingual sentinel, not a Japanese-only copy.
func TestCutIndexEmptyAnchorIsSharedError(t *testing.T) {
	if _, err := cutIndex(nil, ""); !errors.Is(err, agents.ErrNoForkPoint) {
		t.Fatalf("err = %v, want agents.ErrNoForkPoint", err)
	}
	if _, err := cutIndex(nil, "nope"); !errors.Is(err, agents.ErrForkPointNotFound) {
		t.Fatalf("err = %v, want agents.ErrForkPointNotFound", err)
	}
}
