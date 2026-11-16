package status

import (
	"fmt"
	"testing"
)

// A reader that sees a turn end in the status record finds its completion key already written.
// The reconciler settles a scheduled run on the record alone and stores what a late hook for the
// same end must raise under that key; with the key written after the record, a sweep landing
// between the two writes found none, stored nothing, and the late hook broadcast a routed result.
// Polling in a tight loop is the only way to see the window from outside, so this is a guard
// that goes red often, not on every run, when the order is wrong.
func TestTurnEndIsNeverVisibleWithoutItsCompletionKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const rounds = 400
	for i := 0; i < rounds; i++ {
		sid := fmt.Sprintf("order-%d", i)
		seen := make(chan bool, 1)
		go func() {
			for {
				if st, ok := Read(sid); ok && st.TurnEnd {
					_, key := ReadCompletionKey(sid)
					seen <- key
					return
				}
			}
		}()
		PersistTurnEnd(sid, "idle")
		if !<-seen {
			t.Fatalf("round %d: the turn end was readable before its completion key", i)
		}
	}
}
