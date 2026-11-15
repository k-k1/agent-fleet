package sessionx

// kiro's pane scrape has no position to pin an input to, so an input that never reaches the CLI
// is not caught by reconcileInputDelivery (#4047). Its stored "working" must still end for every
// reader once the warm-up is over, without any record of a turn end.

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestKiroStoredWorkingEndsForEveryRouteWhenThePaneSaysNothing(t *testing.T) {
	isolateAgentState(t)
	m := session.Meta{Dir: t.TempDir(), Name: "kiro-stale", Kind: session.KindKiro, CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	oa := fleetAlive
	fleetAlive = func(session.Meta) bool { return true }
	t.Cleanup(func() { fleetAlive = oa })
	// No tmux session exists in a test, so the pane is empty and the source has no opinion.
	status.Persist(sid, "working")

	// Inside the warm-up the optimistic working stands on every route: the prompt may still be
	// booting the CLI.
	if got := DriveState(m, true, false); got != "working" {
		t.Fatalf("fresh: DriveState = %q, want working", got)
	}
	if got := wireSession(m, true).State; got != "working" {
		t.Fatalf("fresh: list = %q, want working", got)
	}

	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(statusPath(sid), old, old); err != nil {
		t.Fatal(err)
	}
	if got := DriveState(m, true, false); got != "idle" {
		t.Fatalf("stale: DriveState = %q, want idle (get_session_status and the chip)", got)
	}
	w := wireSession(m, true)
	if w.State != "" && w.State != "idle" {
		t.Fatalf("stale: list = %q, want idle or blank", w.State)
	}
	if obs, ok := (watchFleet{}).Observe(m.Name); !ok || !obs.Idle {
		t.Fatalf("stale: the idle watch sees idle=%v (ok=%v), want true", obs.Idle, ok)
	}
	// Reading idle is not a turn end: nothing is persisted, stamped or told.
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("record = %+v, want the untouched working", st)
	}
	if w.LastTurnEndAt != "" || status.ObservedTurnEnd(sid) != "" {
		t.Fatalf("a turn end was stamped: list=%q observed=%q", w.LastTurnEndAt, status.ObservedTurnEnd(sid))
	}
}
