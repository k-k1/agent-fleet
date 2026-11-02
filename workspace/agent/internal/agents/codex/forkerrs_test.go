package codex

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestResolveForkAtEmptyAnchorIsSharedError(t *testing.T) {
	_, err := (agentImpl{}).ResolveForkAt(session.Meta{}, agents.ForkPoint{})
	if !errors.Is(err, agents.ErrNoForkPoint) {
		t.Fatalf("err = %v, want agents.ErrNoForkPoint", err)
	}
}
