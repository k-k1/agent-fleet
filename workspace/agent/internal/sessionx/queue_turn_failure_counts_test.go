package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// One predicate decides whether a failed turn is a queue outcome, for both the list's
// lastTurnEndReason and the turn-failed notification id. Pinning its three inputs here keeps
// the two sites from drifting apart again.
func TestQueueTurnFailureCounts(t *testing.T) {
	for _, c := range []struct {
		name string
		m    session.Meta
		want bool
	}{
		{"queue-delivered agy terminal", session.Meta{Kind: session.KindAgy, QueueDelivery: "d1"}, true},
		{"not queue-delivered", session.Meta{Kind: session.KindAgy}, false},
		{"managed driver records its own outcome", session.Meta{Kind: session.KindAgy, QueueDelivery: "d1", Driver: session.DriverManaged}, false},
		{"kind without the trait", session.Meta{Kind: session.KindCopilot, QueueDelivery: "d1"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := queueTurnFailureCounts(c.m); got != c.want {
				t.Fatalf("queueTurnFailureCounts = %v, want %v", got, c.want)
			}
		})
	}
}
