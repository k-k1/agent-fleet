package sessionx

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// agy.LiveState withdraws a stale "working" (no opinion, #1811). The chat chip must not fall
// back to the stored optimistic "working" past the warm-up, or it reads in progress forever,
// and the withdrawal must not look like a turn end.
func TestDriveStateAgyNoOpinionDoesNotKeepStaleWorking(t *testing.T) {
	isolateAgentState(t)
	m := session.Meta{Dir: t.TempDir(), Name: "agystale1", Kind: session.KindAgy}
	sid := session.UUID(m.Dir, m.Name)

	status.Persist(sid, "working")
	if got := DriveState(m, true, true); got != "working" {
		t.Fatalf("fresh stored working: got %q, want working", got)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(statusPath(sid), old, old); err != nil {
		t.Fatal(err)
	}
	if got := DriveState(m, true, true); got != "idle" {
		t.Fatalf("stale stored working: got %q, want idle", got)
	}
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("stored state rewritten to %q: a withdrawn working is not a turn end", st.State)
	}
}
