package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/msp"

// Background work: a tool call the model left running when its turn ended (a `bash` with a
// `yield_time_ms` that later turns poll) keeps the session busy although the host reports it
// idle. MSP has no task-list method and no task-completion notification; what it has is the
// item itself, which stays `inProgress` until its terminal `item/completed`.
//
// Measured on a real stored conversation: the `bash` running `npm test` stayed `inProgress`,
// revision 1, after its turn ended, and the host never sent `background: true` for it. So the
// flag cannot be the signal; a non-terminal toolCall is. While a turn runs every tool call is
// in progress, which is harmless: only an idle state consults the set (BackgroundWork).

// The reasons are claude.BGReason*'s wire values, which the Console maps to its wording; this
// package does not import claude for two strings.
const (
	bgReasonShell   = "shell"
	bgReasonProcess = "process"
)

// bgEntry is one tracked tool call. fromResume marks an entry rebuilt from a resumed history
// rather than seen live: see dropResumedLocked.
type bgEntry struct {
	reason     string
	fromResume bool
}

// bgReason names what a running tool call is, in the same vocabulary as claude's so the Console
// words it the same way. "" means the item is not background work at all.
func bgReason(it msp.Item) string {
	if it.Kind != msp.ItemKindToolCall {
		return ""
	}
	switch str(it.Tool) {
	case "request_user_input":
		// A pending question holds the turn open; it is never work running behind an idle
		// prompt, and a stale one would otherwise light the badge for good.
		return ""
	case "bash", "bash_input":
		return bgReasonShell
	}
	return bgReasonProcess
}

func itemTerminal(s msp.ItemStatus) bool { return s != "" && s != msp.ItemStatusInProgress }

// trackBgLocked folds one live item revision into the set. Caller holds h.mu.
func (h *threadHandle) trackBgLocked(it msp.Item) {
	reason := bgReason(it)
	if reason == "" || it.ItemID == "" {
		return
	}
	if itemTerminal(it.Status) {
		delete(h.bg, it.ItemID)
		return
	}
	if h.bg == nil {
		h.bg = map[string]bgEntry{}
	}
	h.bg[it.ItemID] = bgEntry{reason: reason}
}

// rebuildBgLocked replaces the set with what a resumed history says is still running. The
// history is the HOST's fold from the session/resume result, not AF's own item store: the
// store only knows what reached it, and measured, it holds an `inProgress` tool call whose task
// the host's log records as finished — the terminal revision arrived while nobody was
// listening. Caller holds h.mu.
func (h *threadHandle) rebuildBgLocked(hist msp.SessionHistory) {
	h.bg = nil
	items := hist.Items
	if hist.Mode == msp.HistoryModeSnapshot || hist.Mode == msp.HistoryModeAnchoredSnapshot {
		if hist.Snapshot == nil {
			return
		}
		items = hist.Snapshot.State.Items
	}
	for _, it := range items {
		if r := bgReason(it); r != "" && !itemTerminal(it.Status) && it.ItemID != "" {
			if h.bg == nil {
				h.bg = map[string]bgEntry{}
			}
			h.bg[it.ItemID] = bgEntry{reason: r, fromResume: true}
		}
	}
}

// dropResumedLocked forgets the rebuilt entries the host has not mentioned since the resume,
// once a turn ends. Every resume runs on a freshly spawned host, so a task still `inProgress`
// in its fold may belong to the host that died — which never wrote the terminal record — and
// nothing would ever clear it. A turn ending on the new host with no word about the item is
// taken as that. Caller holds h.mu.
func (h *threadHandle) dropResumedLocked() {
	for id, e := range h.bg {
		if e.fromResume {
			delete(h.bg, id)
		}
	}
}

// backgroundWork reports whether a tracked tool call is still running and what it is; a
// shell command wins over other tools because it is the wording the member recognises.
func (h *threadHandle) backgroundWork() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.alive {
		return false, ""
	}
	reason := ""
	for _, e := range h.bg {
		if reason == "" || e.reason == bgReasonShell {
			reason = e.reason
		}
	}
	return reason != "", reason
}

// BackgroundWork is the live-info read for a named session: whether work from an earlier turn
// is still running, and what it is. Only meaningful while the session is idle — a running turn
// has its own in-progress tool calls. In memory only; no MSP round trip.
func BackgroundWork(name string) (bool, string) {
	h := handleFor(name)
	if h == nil {
		return false, ""
	}
	return h.backgroundWork()
}
