package main

// GET /sessions/usage — per-session context fill and cumulative token consumption,
// aggregated server-side from the transcript layer (the same per-event usage the
// mirror's ContextBar renders). Serves the MCP get_session_usage tool, which is an
// on-demand judgment aid — deliberately NOT folded into /sessions/{name}/status:
// status is a cheap polling primitive, while this reads and folds whole transcripts.

import (
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
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
	// Credits is the metered credit spend (kiro's _kiro.dev/metadata meteringUsage,
	// live-only — a running managed handle's lifetime sum). omitempty ⇒ absent for the
	// token-metered agents. Not tokens, so it lives alongside (not folded into) Spend.
	Credits float64 `json:"credits,omitempty"`
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
	// fold-on-read（docs/log/46 §3-b）: 使用量が読まれたこの機会に、セッション本体の消費を
	// 台帳へ折り込む（60 秒スロットル）。常駐タイマーを増やさないための間借り。
	maybeFoldSessionUsage()
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
// token counts, so aggregateUsage leaves Context nil; but the managed driver holds the
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
	u.Context = &contextUsage{
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

// smallWindowClaudeRe は「200k 側」の Claude だけを列挙する。Claude は Opus 4.6 /
// Sonnet 4.6 以降 1M ネイティブで、今後出るモデルも 1M 前提なので、大きい方を
// 列挙して新モデルのたびに追記する（＝漏れたら 200k に誤認される）運用をやめ、
// 既定 1M・小さいものだけ例外、に反転してある。
//   - haiku 系（4.5 まで 200k）
//   - Claude 3.x 以前（claude-2 / claude-3-*）
//   - Opus 4.0/4.1/4.5・Sonnet 4.0/4.5。日付入りIDは opus-4-20250514 の形なので
//     「4-2」も旧世代側に含める（1M 側の 4-6/4-7/4-8 とは重ならない）。
var smallWindowClaudeRe = regexp.MustCompile(`haiku|claude-[123]|opus-4-[0125]|sonnet-4-[025]`)

// contextWindowGuess mirrors the Console's contextWindow() (ContextBar.tsx — keep
// the two in sync). Order: 272k for GPT-5.x (codex normally records its real
// window, so this is the fallback — e.g. the assistant chat's `codex exec`, whose
// events don't carry it) → 200k for the legacy Claude generations above → 1M for
// every other Claude → for non-Claude unknowns, 200k with a grow-to-fit fallback
// when the observed usage already exceeds it.
func contextWindowGuess(model string, used int) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-5"):
		return 272_000
	case smallWindowClaudeRe.MatchString(m):
		return 200_000
	case strings.Contains(m, "claude"):
		return 1_000_000
	}
	if used > 200_000 {
		return 1_000_000
	}
	return 200_000
}
