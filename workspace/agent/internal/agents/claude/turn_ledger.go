package claude

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// stopContinued is StopContinued, replaceable in tests.
var stopContinued = StopContinued

// PaneMayReopen decides whether a busy pane may flip the stored idle back to working (the
// pane reverse-heal, at both of its sites: WireLive and sessionx.DriveState). The caller has
// already established state==idle and pane.Busy.
//
// A turn the Stop hook closed stays closed: claude keeps its spinner ("… (running Stop hook ·
// 3s · …)" + "esc to interrupt") on screen until the hook exits, a window that opens right
// after our own Stop hook persisted idle, so the pane is not evidence of a new turn there.
// Reopening it left a phantom working that only the 45 s idle self-heal cleared (#1834).
// Only a new UserPromptSubmit (which writes working itself, never reaching here) or a Stop
// that another hook blocked (StopContinued, #1600) puts a closed turn back in progress.
//
// Without a closed-turn record the pane decides, which is what the reverse-heal was made for:
// hooks not wired or the sid unresolved, a record from an older agent (no prompt id), or one
// a heal removed or wrote without a prompt id.
func PaneMayReopen(sid string) bool {
	_, _, ok := paneMayReopen(sid)
	return ok
}

// paneMayReopen is PaneMayReopen that also returns the record it decided from (its Rev, and
// whether there was one), the precondition of ReopenFromPane's write.
func paneMayReopen(sid string) (rev string, existed, ok bool) {
	st, existed := status.Read(sid)
	if !existed || st.State != "idle" || !st.TurnEnd || st.PromptID == "" {
		return st.Rev, existed, true
	}
	at, err := time.Parse(time.RFC3339, st.TS)
	if err != nil {
		return st.Rev, existed, false
	}
	return st.Rev, existed, stopContinued(sid, at)
}

// ReopenFromPane is the pane reverse-heal: when PaneMayReopen allows it, persist working,
// but only if the record is still the one that decision read. The Stop hook is another
// process and may close the turn between the read (and, for StopContinued, a transcript scan)
// and this write; a blind write would erase that closed turn and bring #1834's phantom
// working back. Reports whether working was written; false means leave the state as read.
func ReopenFromPane(sid string) bool {
	rev, existed, ok := paneMayReopen(sid)
	return ok && status.PersistIf(sid, "working", rev, existed)
}
