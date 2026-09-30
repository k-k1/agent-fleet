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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
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

// spendKeep caps the per-turn spends a handle remembers at what the overview card draws
// (claude.TokenSpendMax); older turns would only grow the handle and the list payload.
const spendKeep = 24

// turnSpend is one turn's share of the token trend, folded from its session/tokenUsage
// events: output summed over every model completion, plus the uncached prompt of the LAST
// one — each completion re-states the whole prompt, so summing prompts would count the
// context once per tool call (the Console's spendOf, and sessionx's foldOverviewFacts).
type turnSpend struct {
	turnID   string
	out      int
	uncached int
}

// recordTokenUsage folds one session/tokenUsage into the trend. It keys on the turn id rather
// than on turn/completed, so the trend does not depend on which of the two the host sends
// first, and it looks the id up among every turn it holds: MSP orders events by viewCursor,
// which says nothing about turn ids arriving contiguously, and a late event matched only
// against the newest turn would split its own turn in two and count its prompt twice. A turn
// already dropped past spendKeep stays dropped rather than coming back as the newest.
// The uncached prompt is promptTokens minus the cache read: promptTokens is the server's
// counted-once figure, and inputTokens alone is provider-convention-dependent (ADR 0095 B1-1
// measured the cache inside it). Usage with no turn id is not a reply, so it is skipped.
func (h *threadHandle) recordTokenUsage(p msp.SessionTokenUsageParams) {
	if p.TurnID == "" {
		return
	}
	read := p.Usage.CachedTokens
	if p.Usage.CacheReadTokens != nil {
		read = *p.Usage.CacheReadTokens
	}
	uncached := max(int(p.PromptTokens-read), 0)
	h.ctxMu.Lock()
	defer h.ctxMu.Unlock()
	for i := range h.spends {
		if h.spends[i].turnID == p.TurnID {
			h.spends[i].out += int(p.Usage.OutputTokens)
			h.spends[i].uncached = uncached
			return
		}
	}
	for _, id := range h.spendsDropped {
		if id == p.TurnID {
			return
		}
	}
	h.spends = append(h.spends, turnSpend{turnID: p.TurnID, out: int(p.Usage.OutputTokens), uncached: uncached})
	if over := len(h.spends) - spendKeep; over > 0 {
		for _, s := range h.spends[:over] {
			h.spendsDropped = append(h.spendsDropped, s.turnID)
		}
		if n := len(h.spendsDropped); n > spendKeep {
			h.spendsDropped = append(h.spendsDropped[:0:0], h.spendsDropped[n-spendKeep:]...)
		}
		h.spends = append(h.spends[:0:0], h.spends[over:]...)
	}
}

// resetUsage forgets the context reading and the trend. openSession calls it when the slot
// opens a conversation other than the one the handle has been reading — a fresh start after
// the stored session is gone, or a fork — because the handle outlives its host and the old
// conversation's fill and spends would otherwise stand in for the new one's. A successful
// session/resume keeps them: it is the same conversation.
func (h *threadHandle) resetUsage() {
	h.ctxMu.Lock()
	h.ctxUsed, h.ctxWindow, h.ctxHasUsage = 0, nil, false
	h.spends, h.spendsDropped = nil, nil
	h.ctxMu.Unlock()
}

// ManagedSpends returns the newest per-turn spends of a live managed muse session, oldest
// first; nil when there is no live handle or no turn has reported usage.
//
// It is live-only by necessity: AF's item store carries no usage (measured on 1.4.0: zero
// items with it), and session/resume replays no session/tokenUsage (ADR 0095 B1-1). A host
// respawned into the same conversation keeps the trend on its handle; a restarted Agent has
// no handle and starts it afresh. It is also partial the way decision 10 says:
// subagent and observer model calls never reach session/tokenUsage.
func ManagedSpends(name string) []int {
	h := handleFor(name)
	if h == nil {
		return nil
	}
	h.ctxMu.Lock()
	defer h.ctxMu.Unlock()
	var out []int
	for _, s := range h.spends {
		if n := s.out + s.uncached; n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// overviewContext is the overview card's fill: the same reading and window fallback as
// ContextFill, so a card and its mirror draw the same numbers. MSP gives one counted-once
// figure, so it is a single segment with no cache breakdown.
func overviewContext(name string) *session.ContextUsage {
	used, win, ok := ManagedContext(name)
	if !ok {
		return nil
	}
	c := &session.ContextUsage{Fresh: int(used), Window: MuseDefaultWindow, WindowSource: "estimated"}
	if win != nil && *win > 0 {
		c.Window, c.WindowSource = int(*win), "recorded"
	}
	return c
}
