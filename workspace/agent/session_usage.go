package main

// GET /sessions/usage — per-session context fill and cumulative token consumption,
// aggregated server-side from the transcript layer (the same per-event usage the
// mirror's ContextBar renders). Serves the MCP get_session_usage tool, which is an
// on-demand judgment aid — deliberately NOT folded into /sessions/{name}/status:
// status is a cheap polling primitive, while this reads and folds whole transcripts.

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// contextUsage is the CURRENT context fill: the last main-chain assistant event's
// input snapshot (cache read / cache creation / fresh input), like the Console's
// ContextBar. Absent until the session's first assistant reply; after claude's
// auto-compaction it reflects the post-compaction (smaller) context.
type contextUsage struct {
	Tokens int `json:"tokens"` // read + create + fresh
	Read   int `json:"read"`
	Create int `json:"create"`
	Fresh  int `json:"fresh"`
	Window int `json:"window,omitempty"` // context-window size the pct is against
	// windowSource: "recorded" = the agent reported its real window (codex
	// model_context_window); "estimated" = guessed from the model name.
	WindowSource string  `json:"windowSource,omitempty"`
	Pct          float64 `json:"pct,omitempty"` // 0–100, tokens/window
	Model        string  `json:"model,omitempty"`
}

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
}

type sessionUsage struct {
	Name       string          `json:"name"`
	Display    string          `json:"display"`
	Kind       string          `json:"kind"`
	Context    *contextUsage   `json:"context,omitempty"`
	Cumulative cumulativeUsage `json:"cumulative"`
}

// handleSessionsUsage (GET /sessions/usage?name=<optional>) returns usage for every
// non-archived transcript-capable session (shell/ssm have no transcript), or for one
// session when ?name= is given.
func handleSessionsUsage(w http.ResponseWriter, r *http.Request) {
	nameFilter := r.URL.Query().Get("name")
	if nameFilter != "" && !session.ValidName(nameFilter) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	out := []sessionUsage{}
	for _, m := range session.ListMetas() {
		if m.Archived || (nameFilter != "" && m.Name != nameFilter) {
			continue
		}
		if !agentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		u := aggregateUsage(usageTurns(m))
		u.Name, u.Display, u.Kind = m.Name, session.Display(m), string(m.Kind)
		out = append(out, u)
	}
	if nameFilter != "" && len(out) == 0 {
		httpx.WriteErr(w, http.StatusNotFound, "not_found",
			"no transcript-capable session named "+nameFilter)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// usageTurns loads the full transcript for aggregation: claude via its jsonl
// (TranscriptRead + CollectTurns — the same parse /messages uses), the other
// transcript agents via their Transcript() normalization.
func usageTurns(m session.Meta) []transcript.Turn {
	if m.Kind == session.KindClaude {
		lines, _, _ := claude.TranscriptRead(session.UUID(m.Dir, m.Name))
		return claude.CollectTurns(lines, 0, len(lines))
	}
	td, _ := agentOf(m.Kind).Transcript(m)
	return td.Turns
}

// aggregateUsage folds per-event turns into the wire shape. Pure — unit-tested.
func aggregateUsage(turns []transcript.Turn) sessionUsage {
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
					window, source = contextWindowGuess(t.Model, t.InTok+t.CacheRead+t.CacheCreate), "estimated"
				}
				c := &contextUsage{
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

var bigWindowModelRe = regexp.MustCompile(`opus-4-[678]|sonnet-4-6|fable-5|mythos-5`)

// contextWindowGuess mirrors the Console's contextWindow() (ContextBar.tsx — keep
// the two in sync): current 1M-native families, 272k for GPT-5.x (codex normally
// records its real window, so this is the fallback — e.g. the assistant chat's
// `codex exec`, whose events don't carry it), 200k for haiku, and a grow-to-fit
// fallback when the observed usage already exceeds 200k.
func contextWindowGuess(model string, used int) int {
	m := strings.ToLower(model)
	if bigWindowModelRe.MatchString(m) {
		return 1_000_000
	}
	if strings.Contains(m, "gpt-5") {
		return 272_000
	}
	if strings.Contains(m, "haiku") {
		return 200_000
	}
	if used > 200_000 {
		return 1_000_000
	}
	return 200_000
}
