package sessionx

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// For the hook-less TUI kinds the list reads each kind's own source, which has no opinion
// until it exists (no conversation adopted, no events file, footer not drawn). A prompt just
// sent must still read as working, but the fallback must neither answer idle without a record
// (the reaper would halt a running session) nor keep a stale working alive (the Control Plane
// would never idle-stop the workspace).
func TestWireLiveFallsBackOnlyToFreshWorking(t *testing.T) {
	for _, kind := range []string{session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro} {
		t.Run(kind, func(t *testing.T) {
			isolateAgentState(t)
			m := session.Meta{Dir: t.TempDir(), Name: "fallback-" + kind, Kind: kind}
			sid := session.UUID(m.Dir, m.Name)
			if got := AgentOf(kind).WireLive(m, true).State; got != "" {
				t.Fatalf("no record: got %q, want no opinion", got)
			}
			status.Persist(sid, "working")
			if got := AgentOf(kind).WireLive(m, true).State; got != "working" {
				t.Fatalf("fresh working: got %q, want working", got)
			}
			old := time.Now().Add(-10 * time.Minute)
			if err := os.Chtimes(statusPath(sid), old, old); err != nil {
				t.Fatal(err)
			}
			if got := AgentOf(kind).WireLive(m, true).State; got != "" {
				t.Fatalf("stale working: got %q, want no opinion", got)
			}
		})
	}
}

func statusPath(sid string) string {
	return paths.AgentStateDir() + "/session-status/" + sid + ".json"
}
