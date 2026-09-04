package sessionx

// GET /sessions/usage — per-session context fill and cumulative token consumption,
// aggregated server-side from the transcript layer (the same per-event usage the
// mirror's ContextBar renders). Serves the MCP get_session_usage tool, which is an
// on-demand judgment aid — deliberately NOT folded into /sessions/{name}/status:
// status is a cheap polling primitive, while this reads and folds whole transcripts.

import (
	"math"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// cumulativeUsage sums consumption across the whole transcript. Events between user
// turns are folded into one logical turn the way the Console does: output tokens
// accumulate, input/cache snapshots replace (each event re-reports the full context,
// so summing them raw would double-count). Spend = inTok + cacheCreate + outTok per
// logical turn (the Console's spendOf), summed.
type cumulativeUsage struct {
	Turns       int `json:"turns"` // logical assistant turns (incl. subagent sidechains)
	InTok       int `json:"inTok"`
	OutTok      int `json:"outTok"`
	CacheRead   int `json:"cacheRead"`
	CacheCreate int `json:"cacheCreate"`
	Spend       int `json:"spend"`
	// Credits is the metered credit spend (kiro's _kiro.dev/metadata meteringUsage,
	// live-only — a running managed handle's lifetime sum). omitempty ⇒ absent for the
	// token-metered agents. Not tokens, so it lives alongside (not folded into) Spend.
	Credits float64 `json:"credits,omitempty"`
}

type sessionUsage struct {
	Name       string               `json:"name"`
	Display    string               `json:"display"`
	Kind       string               `json:"kind"`
	Context    *usagex.ContextUsage `json:"context,omitempty"`
	Cumulative cumulativeUsage      `json:"cumulative"`
}

// HandleSessionsUsage (GET /sessions/usage?name=<optional>) returns usage for every
// non-archived transcript-capable session (shell/ssm have no transcript), or for one
// session when ?name= is given.
func HandleSessionsUsage(w http.ResponseWriter, r *http.Request) {
	nameFilter := r.URL.Query().Get("name")
	if nameFilter != "" && !session.ValidName(nameFilter) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// fold-on-read (docs/log/46 §3-b): now that usage is being read, fold the session's own
	// consumption into the ledger (throttled to 60s). Piggybacking here rather than adding
	// another resident timer.
	maybeFoldSessionUsage()
	out := []sessionUsage{}
	for _, m := range session.ListMetas() {
		if m.Archived || (nameFilter != "" && m.Name != nameFilter) {
			continue
		}
		if !AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		u := AggregateUsage(UsageTurns(m))
		u.Name, u.Display, u.Kind = m.Name, session.Display(m), string(m.Kind)
		overlayKiroLiveUsage(m, &u)
		out = append(out, u)
	}
	if nameFilter != "" && len(out) == 0 {
		httpx.WriteErr(w, http.StatusNotFound, "not_found",
			"no transcript-capable session named "+nameFilter)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// overlayKiroLiveUsage fills a running managed kiro session's context + credits from
// its live ACP handle (Track D — docs/log/43 §10). kiro's v2 JSONL transcript carries no
// token counts, so AggregateUsage leaves Context nil; but the managed driver holds the
// latest _kiro.dev/metadata contextUsagePercentage in memory. We convert the % to a
// token count against the model's real context window (so pct is exact and the token
// figure is a faithful estimate), and surface the metered credit spend. No live handle
// / no metadata yet ⇒ unchanged (Context stays nil, credits absent) — honest.
func overlayKiroLiveUsage(m session.Meta, u *sessionUsage) {
	if m.Kind != session.KindKiro {
		return
	}
	pct, window, credits, model, ok := kiro.ManagedContext(m.Name)
	if !ok || window <= 0 {
		return
	}
	tokens := int(math.Round(pct / 100 * float64(window)))
	u.Context = &usagex.ContextUsage{
		Tokens: tokens,
		Fresh:  tokens, // single un-broken-down segment (no cache-read/create split available)
		Window: window,
		// The window is the model's real catalog size, but tokens are derived from the
		// reported %, so mark the count as estimated (the % itself is exact).
		WindowSource: "estimated",
		Pct:          pct,
		Model:        model,
	}
	u.Cumulative.Credits = credits
}

// UsageTurns loads the full transcript for aggregation: claude via its jsonl
// (TranscriptRead + CollectTurns — the same parse /messages uses), the other
// transcript agents via their Transcript() normalization.
func UsageTurns(m session.Meta) []transcript.Turn {
	if m.Kind == session.KindClaude {
		lines, _, _ := claude.TranscriptRead(session.UUID(m.Dir, m.Name))
		return claude.CollectTurns(lines, 0, len(lines))
	}
	td, _ := AgentOf(m.Kind).Transcript(m)
	return td.Turns
}

// AggregateUsage folds per-event turns into the wire shape. Pure — unit-tested.
func AggregateUsage(turns []transcript.Turn) sessionUsage {
	var u sessionUsage
	// Current logical-turn accumulator (folded on a user turn or sidechain flip).
	var curIn, curOut, curRead, curCreate int
	inGroup := false
	sidechain := false
	fold := func() {
		if !inGroup {
			return
		}
		u.Cumulative.Turns++
		u.Cumulative.InTok += curIn
		u.Cumulative.OutTok += curOut
		u.Cumulative.CacheRead += curRead
		u.Cumulative.CacheCreate += curCreate
		u.Cumulative.Spend += curIn + curCreate + curOut
		curIn, curOut, curRead, curCreate = 0, 0, 0, 0
		inGroup = false
	}
	for _, t := range turns {
		if t.Role != "assistant" {
			fold()
			continue
		}
		if t.Sidechain != sidechain {
			fold() // a subagent sidechain reports its OWN context — never merge across
			sidechain = t.Sidechain
		}
		inGroup = true
		curOut += t.OutTok
		if t.InTok+t.CacheRead+t.CacheCreate > 0 {
			curIn, curRead, curCreate = t.InTok, t.CacheRead, t.CacheCreate
			if !t.Sidechain {
				// Main-chain snapshot = the session's current context fill. A sidechain
				// event's snapshot is the SUBAGENT's context, not this session's.
				window, source := t.CtxWindow, "recorded"
				if window <= 0 {
					window, source = usagex.WindowGuess(t.Model, t.InTok+t.CacheRead+t.CacheCreate), "estimated"
				}
				c := &usagex.ContextUsage{
					Tokens: t.InTok + t.CacheRead + t.CacheCreate,
					Read:   t.CacheRead, Create: t.CacheCreate, Fresh: t.InTok,
					Window: window, WindowSource: source, Model: t.Model,
				}
				if window > 0 {
					c.Pct = float64(c.Tokens) / float64(window) * 100
				}
				u.Context = c
			}
		}
	}
	fold()
	return u
}
