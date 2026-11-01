package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// editedUsageLimitAgent keeps a registered agent's terminal and live-state traits and passes its
// usage-limit traits through edit.
type editedUsageLimitAgent struct {
	agents.Agent
	edit func(*agents.UsageLimitTraits)
}

func (e editedUsageLimitAgent) TerminalTraits() agents.TerminalTraits {
	if t, ok := e.Agent.(agents.TerminalTraiter); ok {
		return t.TerminalTraits()
	}
	return agents.TerminalTraits{}
}

func (e editedUsageLimitAgent) LiveStateTraits() agents.LiveStateTraits {
	if l, ok := e.Agent.(agents.LiveStater); ok {
		return l.LiveStateTraits()
	}
	return agents.LiveStateTraits{}
}

func (e editedUsageLimitAgent) UsageLimitTraits() agents.UsageLimitTraits {
	var tr agents.UsageLimitTraits
	if u, ok := e.Agent.(agents.UsageLimiter); ok {
		tr = u.UsageLimitTraits()
	}
	e.edit(&tr)
	return tr
}

// editUsageLimitTraits registers kind's agent with its usage-limit traits passed through edit,
// until the test ends: how a test stands in for a kind's transcript, handle or account windows.
func editUsageLimitTraits(t *testing.T, kind string, edit func(*agents.UsageLimitTraits)) {
	t.Helper()
	t.Cleanup(agentreg.SwapForTest(kind, editedUsageLimitAgent{agentreg.Of(kind), edit}))
}

// MoveTarget forwards the wrapped kind's move declaration (do not swap a kind that makes none:
// the wrapper then answers 0 where the kind has no method; other test doubles do not forward); without it the wrapper hides the
// optional capability and the kind stops being a move target for the test's duration. 0 is
// "no declaration".
func (e editedUsageLimitAgent) MoveTarget() agents.MoveTargetState {
	if m, ok := e.Agent.(agents.MoveTarget); ok {
		return m.MoveTarget()
	}
	return 0
}
