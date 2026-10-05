package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// The mirror/chat chip reads DriveState, the list badge reads WireLive. A muse turn waiting on
// a question has only ever written "working" to the status file, so the chip said "in
// progress" over a question card until DriveState asked the live handle too.
func TestDriveStateMusePendingPrompt(t *testing.T) {
	isolateAgentState(t)
	m := session.Meta{Name: "musepq1", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged}
	session.WriteMeta(m)
	status.Persist(session.UUID(m.Dir, m.Name), "working")
	pending := ""
	prev := musePendingState
	musePendingState = func(name string) string {
		if name != m.Name {
			return ""
		}
		return pending
	}
	t.Cleanup(func() { musePendingState = prev })

	for _, c := range []struct{ pending, want string }{
		{"", "working"}, {"question", "question"}, {"permission", "permission"}, {"", "working"},
	} {
		pending = c.pending
		for _, heal := range []bool{true, false} {
			if got := DriveState(m, true, heal); got != c.want {
				t.Errorf("pending %q, heal %v: DriveState = %q, want %q", c.pending, heal, got, c.want)
			}
		}
	}
}
