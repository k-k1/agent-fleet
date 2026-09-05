package kiro

// Session-level context fill for the chat mirror's ContextBar (Track D, docs/log/43 §10).
// kiro's v2 JSONL transcript carries no per-turn token counts (unlike claude/codex), so the
// claude-style transcript-derived ContextBar is impossible. Instead we use the live
// contextUsagePercentage the managed (ACP) driver carries in `_kiro.dev/metadata` notifications.
// It is the same agents.ContextReporter seam agy uses to PTY-scrape /context, but kiro keeps the
// value in memory on the live handle, so it answers immediately with no subprocess and no block.
//
// Percent-to-token conversion: kiro hands us a percentage, while the existing ContextBar is token
// based (it draws read/create/fresh against the window). So the percentage is converted into a
// token count against the model's real context window (from the catalogue) and shown as a single
// segment. The window is passed explicitly, so a percentage the frontend recomputes from
// tokens/window matches the original exactly, up to rounding. A managed paneless session has the
// mirror as its only view, which makes this the primary display of live context.
//
// On TUI (Terminal execution) or before any metadata has arrived, ManagedContext returns ok=false
// and this function returns nil, so no ContextBar is drawn - with no source for the percentage,
// hiding it is the honest answer.

import (
	"math"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ContextFill implements agents.ContextReporter for the generic /messages handler
// (the chat mirror's poll). Returns nil unless a live managed handle has reported at
// least one _kiro.dev/metadata percentage.
func (agentImpl) ContextFill(m session.Meta) *transcript.Context {
	pct, window, _, _, ok := ManagedContext(m.Name)
	if !ok || window <= 0 {
		return nil
	}
	tokens := int(math.Round(pct / 100 * float64(window)))
	return &transcript.Context{
		Tokens: tokens,
		Window: window,
		At:     time.Now().UTC().Format(time.RFC3339),
	}
}
