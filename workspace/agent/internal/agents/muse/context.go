package muse

// Session-level context fill for the chat mirror's ContextBar (ADR 0095 P2-16).
// MSP emits session/contextUsage around every turn: usedTokens (exact counted-once occupancy)
// and optional windowTokens (absent when the basis carries no limit — never fabricate it).
//
// The window constant below is the measured muse-spark value across all four models.
// When windowTokens arrives on the wire that value wins; the constant is a fallback so
// the bar is usable even before a window-carrying notification has arrived.
//
// The SAME sessionUsage.Context path used for kiro/agy (ContextReporter + the bulk
// overlayMuseLiveUsage in session_usage.go) is re-used here. The difference from kiro is
// that MSP delivers exact token counts rather than a percentage, so no pct-to-token
// conversion is needed.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// MuseDefaultWindow is the measured context-window size for every muse-spark model
// (all four variants report 1,007,997 tokens). Used as a fallback when windowTokens
// is absent from the notification (basis has no limit) or no notification has arrived yet.
const MuseDefaultWindow = 1_007_997

// ManagedContext returns the live context fill for a managed muse session. usedTokens is
// the exact counted-once occupancy from the latest session/contextUsage notification.
// windowTokens is nil when the wire did not carry one; callers that need a concrete window
// fall back to museDefaultWindow. ok=false when no live handle exists or no notification
// has arrived yet — so a TUI session or a pre-first-turn managed session shows no bar.
func ManagedContext(name string) (usedTokens int64, windowTokens *int64, ok bool) {
	h := handleFor(name)
	if h == nil {
		return 0, nil, false
	}
	h.ctxMu.Lock()
	defer h.ctxMu.Unlock()
	if !h.ctxHasUsage {
		return 0, nil, false
	}
	return h.ctxUsed, h.ctxWindow, true
}

// ContextFill implements agents.ContextReporter for the chat mirror's /messages poll.
// Returns nil until the first session/contextUsage notification arrives.
func (agentImpl) ContextFill(m session.Meta) *transcript.Context {
	used, win, ok := ManagedContext(m.Name)
	if !ok {
		return nil
	}
	window := MuseDefaultWindow
	if win != nil && *win > 0 {
		window = int(*win)
	}
	return &transcript.Context{
		Tokens: int(used),
		Window: window,
		At:     time.Now().UTC().Format(time.RFC3339),
	}
}
