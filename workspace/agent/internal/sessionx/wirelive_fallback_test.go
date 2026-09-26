package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// For the hook-less TUI kinds the list reads each kind's own source, which has no opinion
// until it exists (no conversation adopted, no events file, footer not drawn). The list must
// then fall back to the stored status like DriveState does; an empty State is drawn as
// waiting for input while the chat chip says working.
func TestWireLiveFallsBackToStoredStatus(t *testing.T) {
	for _, kind := range []string{session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro} {
		t.Run(kind, func(t *testing.T) {
			isolateAgentState(t)
			m := session.Meta{Dir: t.TempDir(), Name: "fallback-" + kind, Kind: kind}
			status.Persist(session.UUID(m.Dir, m.Name), "working")
			if got := AgentOf(kind).WireLive(m, true).State; got != "working" {
				t.Fatalf("got %q, want working", got)
			}
		})
	}
}
