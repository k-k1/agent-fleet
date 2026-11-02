package copilot

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestCutIndexAtErrorsAreShared(t *testing.T) {
	if _, err := cutIndexAt(nil, ""); !errors.Is(err, agents.ErrNoForkPoint) {
		t.Fatalf("err = %v, want agents.ErrNoForkPoint", err)
	}
	if _, err := cutIndexAt(nil, "nope"); !errors.Is(err, agents.ErrForkPointNotFound) {
		t.Fatalf("err = %v, want agents.ErrForkPointNotFound", err)
	}
}
