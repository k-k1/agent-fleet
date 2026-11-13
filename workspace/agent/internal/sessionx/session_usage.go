package sessionx

// GET /sessions/usage — per-session context fill and cumulative token consumption,
// aggregated server-side from the transcript layer (the same per-event usage the
// mirror's ContextBar renders). Serves the MCP get_session_usage tool, which is an
// on-demand judgment aid — deliberately NOT folded into /sessions/{name}/status:
// status is a cheap polling primitive, while this reads and folds whole transcripts.

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
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
	// Subagents is the part of Cumulative that the session's subagents and Workflow agents
	// spent (claude only; absent when it delegated nothing).
	Subagents *cumulativeUsage `json:"subagents,omitempty"`
}

func (c *cumulativeUsage) add(o cumulativeUsage) {
	c.Turns += o.Turns
	c.InTok += o.InTok
	c.OutTok += o.OutTok
	c.CacheRead += o.CacheRead
	c.CacheCreate += o.CacheCreate
	c.Spend += o.Spend
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
	var metas []session.Meta
	for _, m := range usageMetas() {
		if nameFilter == "" || m.Name == nameFilter {
			metas = append(metas, m)
		}
	}
	var out []sessionUsage
	pending := 0
	if nameFilter != "" {
		// One named session is read to the end: there is no partial answer to give for it.
		for _, m := range metas {
			out = append(out, completeUsage(m, mainUsage.get(m, time.Now())))
		}
	} else {
		out, pending = allSessionsUsage(metas, mainUsageColdBudget)
	}
	if out == nil {
		out = []sessionUsage{}
	}
	if nameFilter != "" && len(out) == 0 {
		httpx.WriteErr(w, http.StatusNotFound, "not_found",
			"no transcript-capable session named "+nameFilter)
		return
	}
	resp := map[string]any{"sessions": out}
	if pending > 0 {
		// Sessions left out because their transcripts were not parsed yet; ask again shortly.
		resp["pending"] = pending
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// completeUsage turns a session's main-transcript fold into its entry in the usage read: the
// subagent folds and the live overlays.
func completeUsage(m session.Meta, u sessionUsage) sessionUsage {
	addSubagentUsage(&u, m)
	u.Name, u.Display, u.Kind = m.Name, session.Display(m), string(m.Kind)
	overlayKiroLiveUsage(m, &u)
	overlayMuseLiveUsage(m, &u)
	return u
}

// usagePass is one walk over every transcript-capable session that folds each session's usage
// into the caches and records the result. At most one runs at a time: every all-sessions read and
// every warm-up joins the running pass instead of starting its own, so repeated calls during a
// slow or stalled read (the tool tells callers to ask again while sessions are pending) never add
// walkers, and a session's subagent transcripts are never parsed by two walkers at once. A pass
// has no deadline and always runs to the end; that is the background warming. Readers bound only
// their own wait.
type usagePass struct {
	done chan struct{}
	mu   sync.Mutex
	res  map[string]sessionUsage // by usagePassKey
}

var (
	usagePassMu  sync.Mutex
	usagePassCur *usagePass
)

// usagePassKey includes CreatedAt: a recreated name is a new session and gets no stale figure.
func usagePassKey(m session.Meta) string { return m.Name + "\x00" + m.CreatedAt }

// joinUsagePass returns the running pass, or starts one over metas when none is running.
func joinUsagePass(metas []session.Meta) *usagePass {
	usagePassMu.Lock()
	defer usagePassMu.Unlock()
	if usagePassCur != nil {
		return usagePassCur
	}
	p := &usagePass{done: make(chan struct{}), res: make(map[string]sessionUsage, len(metas))}
	usagePassCur = p
	go p.run(metas)
	return p
}

func (p *usagePass) run(metas []session.Meta) {
	defer func() {
		usagePassMu.Lock()
		usagePassCur = nil
		usagePassMu.Unlock()
		close(p.done)
	}()
	for _, m := range metas {
		u := completeUsage(m, mainUsage.get(m, time.Now()))
		p.mu.Lock()
		p.res[usagePassKey(m)] = u
		p.mu.Unlock()
	}
}

// allSessionsUsage answers from the running pass (starting one if none is) when it finishes or
// budget runs out, whichever is first; the sessions it has not reached by then are pending. The
// bound covers the whole of every session's work, not only the main-transcript parse: the cold
// subagent fold ran after the budget check for every session and took a cold call to 28 s against
// an 8 s budget, and a parse that has started cannot be cut short by any check between files. The
// request waits on nothing but the timer and a mutex held only to copy results, never on a cache
// lock or a parse.
//
// A joined pass may have started before this call, so a figure can be as old as the pass; a
// session it did not list (created since) is pending until the next pass.
func allSessionsUsage(metas []session.Meta, budget time.Duration) ([]sessionUsage, int) {
	p := joinUsagePass(metas)
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]sessionUsage, 0, len(metas))
	for _, m := range metas {
		if u, ok := p.res[usagePassKey(m)]; ok {
			out = append(out, u)
		}
	}
	return out, len(metas) - len(out)
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

// overlayMuseLiveUsage fills a running managed muse session's context from the live
// MSP handle (ADR 0095 P2-16). MSP delivers session/contextUsage around every turn:
// usedTokens is exact (counted-once) and windowTokens is the host's basis limit, absent
// when the basis has no limit. No live handle / no notification yet ⇒ unchanged — honest.
func overlayMuseLiveUsage(m session.Meta, u *sessionUsage) {
	if m.Kind != session.KindMuse {
		return
	}
	used, win, ok := muse.ManagedContext(m.Name)
	if !ok {
		return
	}
	window := muse.MuseDefaultWindow
	windowSource := "estimated"
	if win != nil && *win > 0 {
		window = int(*win)
		windowSource = "recorded"
	}
	tokens := int(used)
	var pct float64
	if window > 0 {
		pct = float64(tokens) / float64(window) * 100
	}
	u.Context = &usagex.ContextUsage{
		Tokens:       tokens,
		Fresh:        tokens, // single segment — MSP carries no cache-read/create breakdown
		Window:       window,
		WindowSource: windowSource,
		Pct:          pct,
	}
}

// UsageTurns loads the full transcript for aggregation: the kinds with a UsageReader (claude:
// every turn, usage fields only; opencode: billed calls with nothing to display), the rest via
// their Transcript() normalization.
func UsageTurns(m session.Meta) []transcript.Turn {
	if ur, ok := AgentOf(m.Kind).(agents.UsageReader); ok {
		return ur.UsageTurns(m)
	}
	td, _ := AgentOf(m.Kind).Transcript(m)
	return td.Turns
}

// TextTurns is the transcript for a reader of its text (the search index): a kind with a
// TextReader gives its own, otherwise it is UsageTurns.
func TextTurns(m session.Meta) []transcript.Turn {
	if tr, ok := AgentOf(m.Kind).(agents.TextReader); ok {
		return tr.TextTurns(m)
	}
	return UsageTurns(m)
}

// SubagentLogs lists the session's subagent transcripts grouped by agent (every copy of one
// agent's transcript in one group), nil for a kind that keeps none apart.
func SubagentLogs(m session.Meta) [][]string {
	if sr, ok := AgentOf(m.Kind).(agents.SubagentUsageReader); ok {
		return sr.SubagentLogs(m)
	}
	return nil
}

// SubagentTurns parses one agent's group from SubagentLogs, every turn marked Sidechain.
func SubagentTurns(m session.Meta, paths []string) ([]transcript.Turn, error) {
	if sr, ok := AgentOf(m.Kind).(agents.SubagentUsageReader); ok {
		return sr.SubagentTurns(paths)
	}
	return nil, nil
}

// SubagentSource is one subagent transcript as the ledger fold reads it: a stable id, a stamp
// that changes whenever a copy does, and a loader, so an unchanged file is never parsed.
type SubagentSource struct {
	ID      string
	Stamp   string // size and mtime of every copy
	ModTime time.Time
	Turns   func() ([]transcript.Turn, error)
}

// SubagentUsageSources lists the session's subagent transcripts for the ledger fold, from the
// same discovery the usage fold uses. A group whose files are gone is left out.
func SubagentUsageSources(m session.Meta) []SubagentSource {
	sr, ok := AgentOf(m.Kind).(agents.SubagentUsageReader)
	if !ok {
		return nil
	}
	var out []SubagentSource
	for _, paths := range sr.SubagentLogs(m) {
		live, stamp, mod := liveStamp(paths)
		if len(live) == 0 {
			continue
		}
		out = append(out, SubagentSource{
			ID: sr.SubagentID(paths), Stamp: stamp, ModTime: mod,
			Turns: func() ([]transcript.Turn, error) { return sr.SubagentTurns(live) },
		})
	}
	return out
}

// subagentUsageFolds caches each subagent's folded consumption for the all-sessions usage read
// (see SubagentFolds).
var subagentUsageFolds = NewSubagentFolds[cumulativeUsage](usageCacheIdle)

// addSubagentUsage adds the subagents' consumption to the session's cumulative figure and
// reports it on its own as well. Each agent is folded alone: every event re-reports only its
// own agent's context, so folding two agents' interleaved events together would keep one
// agent's input snapshot and drop the other's. An agent that cannot be read is left out: this
// is an on-demand figure, read again on the next call.
func addSubagentUsage(u *sessionUsage, m session.Meta) {
	agents := subagentUsageFolds.Fold(m, time.Now(), func(turns []transcript.Turn) cumulativeUsage {
		return AggregateUsage(turns).Cumulative
	})
	addSubagentCumulative(u, agents)
}

func addSubagentCumulative(u *sessionUsage, agents []cumulativeUsage) {
	var sub cumulativeUsage
	for _, c := range agents {
		sub.add(c)
	}
	if sub == (cumulativeUsage{}) {
		return
	}
	u.Cumulative.add(sub)
	u.Subagents = &sub
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
		if t.CostOnly {
			continue // tokens only here, and an accounting row must not split a logical turn
		}
		if claude.IsCLIWarning(t) {
			continue // no model response and no prompt either, so it neither counts nor splits a turn
		}
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

// UsageMissHeld reports that the usage fold may skip m this pass: the kind remembers a recent
// pass that found no transcript and no subagent transcripts (agents.UsageMissMemo). Only the
// usage fold may ask.
func UsageMissHeld(m session.Meta) bool {
	mm, ok := AgentOf(m.Kind).(agents.UsageMissMemo)
	return ok && mm.UsageMissHeld(m)
}

// UsageMissNote records the miss UsageMissHeld later answers from.
func UsageMissNote(m session.Meta) {
	if mm, ok := AgentOf(m.Kind).(agents.UsageMissMemo); ok {
		mm.UsageMissNote(m)
	}
}

// UsageSourceOf is the transcript the usage fold reads, with a short digest of what it covers
// (empty: nothing to fingerprint) and a loader whose ok=false means the turns may be stale. A
// kind without agents.UsageSourcer is read by UsageTurns, never skippable and always ok.
func UsageSourceOf(m session.Meta) (stamp string, turns func() ([]transcript.Turn, bool)) {
	if us, ok := AgentOf(m.Kind).(agents.UsageSourcer); ok {
		if src, ok := us.UsageSource(m); ok {
			if src.Stamp != "" {
				sum := sha256.Sum256([]byte(src.Stamp))
				stamp = hex.EncodeToString(sum[:8])
			}
			return stamp, src.Turns
		}
	}
	return "", func() ([]transcript.Turn, bool) { return UsageTurns(m), true }
}
