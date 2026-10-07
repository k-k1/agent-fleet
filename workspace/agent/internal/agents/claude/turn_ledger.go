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
	st, ok := status.Read(sid)
	if !ok || st.State != "idle" || !st.TurnEnd || st.PromptID == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, st.TS)
	if err != nil {
		return false
	}
	return stopContinued(sid, at)
}
