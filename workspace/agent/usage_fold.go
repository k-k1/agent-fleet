package main

// Folds a session's own usage from its transcript into the ledger (docs/log/46 §3-b /
// ADR0029 §5).
//
// A session's consumption is produced by a separate process (the CLI), so it cannot be
// recorded at call time the way an auxiliary call is. Instead the transcript is read, only
// the delta is folded, and a watermark makes that idempotent.
//
// Two preconditions keep it from double counting:
//  1. Only registered sessions (session.Meta) are folded. The assistant conversation writes
//     a transcript into claude's projects tree, but has no Meta, so it never mixes in.
//  2. The open trailing logical turn is not folded. If events are appended to that turn
//     after it was folded, the input snapshot (replacement semantics) is counted twice.
//     It is settled when the next user turn closes it, or on session deletion
//     (includeTrailing).
//
// No extra resident timer (memory-constrained host, docs/log/26). The triggers are a
// fold-on-read piggybacked on GET /sessions/usage (throttled to 60s) and the settle on
// deletion.

import (
	"encoding/json"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// usageFoldMark is the watermark for one session.
//
// A count (Groups) alone is not enough. claude can hold sibling jsonl files for one sid (a
// cwd change, a CLAUDE_CONFIG_DIR switch, the Remote Control stub), and the one that gets
// read is chosen by mtime (internal/agents/claude/transcript.go). When that choice flips,
// a count-based delta breaks in two ways: flipping to the shorter file makes
// `len(rows) <= Groups` stop the fold forever, and flipping to the longer one drops
// another conversation's leading turns as already folded. So an unfolded turn is decided
// by both the count and the timestamp — LastTS is not diagnostic, it decides.
type usageFoldMark struct {
	// Groups is the number of logical turns folded so far, i.e. the highest sequence
	// number reached. The transcript is append-only, so that sequence is stable across
	// kinds. Each kind's Turn.Idx (a line number) is not used because the numbering
	// scheme differs per kind and would mix watermarks.
	Groups int `json:"groups"`
	// LastTS is the greatest folded turn timestamp. Even when the transcript is swapped
	// out, it still says "turns newer than this have not been counted".
	LastTS string `json:"lastTS,omitempty"`
	// LastIdx is the transcript line number of the last folded event (diagnostic).
	LastIdx int `json:"lastIdx,omitempty"`
	// OrphanKeys are the OrphanKey of every cost-only row folded so far. They sit outside the
	// logical turn sequence, so neither a count nor a timestamp can say which are new; Groups and
	// LastTS ignore them. Rare (a billed call with nothing to display, alone in its turn).
	OrphanKeys []string `json:"orphanKeys,omitempty"`
	// Uncapped marks a watermark whose Groups counts from the first turn of the whole
	// transcript. Marks written while the claude reader was capped at the newest 1 MiB lack it:
	// their Groups counts from the first VISIBLE turn, a number that shifts once the head
	// becomes visible (see rebaseCappedMark).
	Uncapped bool `json:"uncapped,omitempty"`
}

type usageFoldState struct {
	Sessions map[string]usageFoldMark `json:"sessions"`
	// Subagents is one watermark per subagent / Workflow agent transcript, keyed by
	// subagentMarkKey. Each agent is its own row sequence (its own Idx numbering and its own
	// dedup entry), so folding them never renumbers a turn of the session's main sequence.
	Subagents map[string]usageAgentMark `json:"subagents,omitempty"`
}

// usageAgentMark is the watermark of one subagent transcript. It is identity-based, not a
// count: the copies of an agent are merged by timestamp, so a later copy can put an older turn
// in front, or fill in events of a turn already recorded, and the newest turn grows after it
// was recorded. A turn is told apart by the uuid of its first or last event, and what was
// written for it is kept, so every fold writes exactly the difference.
type usageAgentMark struct {
	// Seq numbers the rows written for this agent: the Idx of the next row is Seq+1. It is the
	// ledger's own replay guard (usage_dedup.go), unrelated to the transcript's order.
	Seq int `json:"seq"`
	// Turns is every turn recorded so far, with the totals written for it.
	Turns []usageAgentTurn `json:"turns,omitempty"`
	// Pending is the batch being written: it is saved before the append, so a crash or a failed
	// save in between replays the identical rows (same Idx, same time), which the ledger's
	// dedup drops when they had already landed. Recomputing the difference instead would pick
	// up growth in between and write a different row under the same Idx.
	Pending []usagex.Record `json:"pending,omitempty"`
	// Stamp is the transcript's size and mtime at the last fold: while it is unchanged the file
	// is not read again.
	Stamp string `json:"stamp,omitempty"`
}

// usageAgentTurn is one recorded turn: the hashes of its first and last event ids and the
// totals written for it so far.
type usageAgentTurn struct {
	First       string  `json:"f"`
	Last        string  `json:"l,omitempty"`
	In          int     `json:"in,omitempty"`
	Out         int     `json:"out,omitempty"`
	CacheRead   int     `json:"cr,omitempty"`
	CacheCreate int     `json:"cc,omitempty"`
	CostUSD     float64 `json:"cost,omitempty"`
}

// There are three separate locks. Merging them would undo the point of making
// fold-on-read asynchronous:
//
//   - usageFoldMu serializes reads and writes of state.json together with the fold of ONE
//     session. The bulk fold always releases it at each session boundary, so a concurrent
//     /usage/series, /sessions/usage or settle-on-delete waits for at most one session.
//     Holding it for a whole pass (measured ~20s over 158 sessions) blocked the second and
//     third of the three /usage/series calls the Console fires from a single screen.
//   - usageFoldRunning is the re-entry guard. It must be readable without taking a lock: a
//     call that only asks whether a fold is running must not queue behind the running one.
//   - usageFoldGate is a tiny lock protecting only the throttle timestamp (held for a few
//     instructions).
var (
	usageFoldMu      sync.Mutex  // state.json reads/writes and the fold of one session
	usageFoldRunning atomic.Bool // a bulk fold is running (lock-free re-entry guard)
	usageFoldGate    sync.Mutex  // guards usageFoldedAt only
	usageFoldedAt    time.Time   // last fold-on-read (throttle; touched under usageFoldGate)
	usageFoldPeriod  = time.Minute
)

func usageStatePath() string { return filepath.Join(usagex.Dir(), "state.json") }

func readUsageFoldState() usageFoldState {
	st := usageFoldState{Sessions: map[string]usageFoldMark{}, Subagents: map[string]usageAgentMark{}}
	b, err := os.ReadFile(usageStatePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil || st.Sessions == nil {
		st.Sessions = map[string]usageFoldMark{}
	}
	if st.Subagents == nil {
		st.Subagents = map[string]usageAgentMark{}
	}
	return st
}

// writeUsageFoldState replaces the watermark with tmp+rename. Errors are never swallowed:
// proceeding as if a failed write had succeeded makes the next pass re-append the rows
// already written, i.e. double counting.
func writeUsageFoldState(st usageFoldState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(usagex.Dir(), 0o700); err != nil {
		return err
	}
	tmp := usageStatePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, usageStatePath()) // a crash mid-write must not leave broken state
}

// usageTurnRow is one logical turn folded out of a transcript. It is the output of a pure
// function, so it can be unit-tested.
type usageTurnRow struct {
	Idx       int // logical turn sequence number (1-based)
	TS        string
	Model     string
	Trigger   string // derived from the injection source of the preceding user turn
	Sidechain bool
	Tokens    usagex.Tokens
	// CostUSD is the agent-reported cost of the turn (transcript.Turn.CostUSD). Unlike the
	// input tokens it is additive: each row is one LLM call's own cost, never a re-report.
	CostUSD float64
	LastIdx int // transcript line number of the last event (diagnostic)
	// ID is the uuid of the turn's first event: what tells one turn from another when the
	// transcript is read again with a different set of lines (subagent copies merged).
	ID string
	// LastID is the uuid of its last event. A copy found later can add events at either end, so
	// a turn is recognised by either id.
	LastID string
	// Orphan marks a cost-only row: the reported cost of CostOnly turns that no logical turn on
	// their side took in before the next boundary. It is outside the logical turn sequence (Idx
	// stays 0, so the watermark's Groups never moves) and is identified by OrphanKey, the
	// segment's last message id: stable across polls, unique across conversations, so a session
	// that moves to another conversation still folds its new ones, and the ledger's Key dedup
	// drops a re-append.
	Orphan    bool
	OrphanKey string
}

// foldTurnRows folds a transcript's event sequence into logical turns. The aggregation
// must match aggregateUsage (session_usage.go) exactly: output tokens add up, while input
// and cache replace, because every event re-reports the whole context. A divergence here
// makes the ledger and get_session_usage disagree, i.e. two screens showing different
// numbers.
//
// With includeTrailing=false, a group that never closed is not returned (a turn still in
// progress).
//
// A CostOnly turn (transcript.Turn.CostOnly) never opens, closes or splits a logical turn. Its
// cost joins the open logical turn on its side, else the next one on its side before the next
// boundary; failing both it becomes an Orphan row at that boundary (or at the end, with
// includeTrailing). A closed segment is final, so where a cost lands never changes later.
func foldTurnRows(turns []transcript.Turn, includeTrailing bool) []usageTurnRow {
	var rows []usageTurnRow
	var cur usageTurnRow
	inGroup, sidechain := false, false
	trigger := usagex.TriggerUser
	groups := 0
	var pending [2]usageTurnRow // CostOnly cost waiting for a logical turn, [main, sidechain]
	side := func(sc bool) int {
		if sc {
			return 1
		}
		return 0
	}
	fold := func() {
		if !inGroup {
			return
		}
		groups++
		cur.Idx = groups
		rows = append(rows, cur)
		cur, inGroup = usageTurnRow{}, false
	}
	flush := func() {
		for i := range pending {
			if p := pending[i]; p.CostUSD > 0 {
				p.Orphan = true
				if p.OrphanKey == "" { // no message id: the best stable stand-in there is
					p.OrphanKey = fmt.Sprintf("ts:%s#%d", p.TS, p.LastIdx)
				}
				rows = append(rows, p)
			}
			pending[i] = usageTurnRow{}
		}
	}
	for _, t := range turns {
		if t.CostOnly {
			if inGroup && cur.Sidechain == t.Sidechain {
				cur.CostUSD += t.CostUSD
				continue
			}
			p := &pending[side(t.Sidechain)]
			if p.CostUSD == 0 {
				*p = usageTurnRow{Trigger: trigger, Sidechain: t.Sidechain}
			}
			p.CostUSD += t.CostUSD
			if t.TS != "" {
				p.TS = t.TS
			}
			if t.Model != "" {
				p.Model = t.Model
			}
			p.LastIdx = t.Idx
			if t.AnchorID != "" {
				p.OrphanKey = t.AnchorID
			}
			continue
		}
		if t.Role != "assistant" {
			fold()
			flush()
			if t.Role == "user" && !t.Compact {
				trigger = usageTriggerFromTurnSource(t.Source)
			}
			continue
		}
		if t.Sidechain != sidechain {
			fold() // a subagent reports its own context — never fold across the boundary
			sidechain = t.Sidechain
		}
		if !inGroup {
			cur = usageTurnRow{Trigger: trigger, Sidechain: sidechain, ID: t.AnchorID}
			inGroup = true
			if p := &pending[side(sidechain)]; p.CostUSD > 0 {
				cur.CostUSD, *p = p.CostUSD, usageTurnRow{}
			}
		}
		cur.Tokens.Out += t.OutTok
		cur.CostUSD += t.CostUSD
		if t.InTok+t.CacheRead+t.CacheCreate > 0 {
			cur.Tokens.In, cur.Tokens.CacheRead, cur.Tokens.CacheCreate = t.InTok, t.CacheRead, t.CacheCreate
		}
		if t.TS != "" {
			cur.TS = t.TS
		}
		if t.Model != "" {
			cur.Model = t.Model
		}
		cur.LastIdx = t.Idx
		if cur.ID == "" {
			cur.ID = t.AnchorID
		}
		if t.AnchorID != "" {
			cur.LastID = t.AnchorID
		}
	}
	if includeTrailing {
		fold()
		flush()
	}
	return rows
}

// usageTriggerFromTurnSource maps the injection source (transcript.Turn.Source) onto the
// ledger's trigger vocabulary. The table exists to separate consumption by a turn a person
// typed from one pushed in by the operator, a bridge or the scheduler.
func usageTriggerFromTurnSource(src string) string {
	switch src {
	case sessionx.TurnSourceOperator:
		return usagex.TriggerOperator
	case sessionx.TurnSourceDiscord, sessionx.TurnSourceSlack:
		return usagex.TriggerBridge
	case sessionx.TurnSourceSchedule, sessionx.TurnSourceScheduleManual:
		return usagex.TriggerSchedule
	case sessionx.TurnSourceAutoResume, sessionx.TurnSourceNotice:
		// auto-resume after an abort (docs/log/47 §4-6) is self-repair consumption; the
		// stalled-tree notice is the Agent's own nudge, not a person's turn, in the same way
		return usagex.TriggerRecovery
	}
	return usagex.TriggerUser
}

// usageMeasuredForKind is the measurement accuracy per kind (docs/log/46 §1-c). It is a
// self-declaration that stops the 0 of an agent that reports no tokens from reading as
// "consumed nothing"; the UI uses it to show unmeasured consumption separately.
func usageMeasuredForKind(kind string) string {
	switch kind {
	case session.KindClaude, session.KindCodex, session.KindOpencode:
		return usagex.MeasuredExact
	// lcpp (ADR 0093 decision 8): llama-server's own `usage` (prompt_tokens/completion_tokens)
	// is exact and rides the stream (CP gateway's askForStreamUsage forces
	// stream_options.include_usage), and this kind never runs it through usagex.WindowGuess or
	// any other estimator — it IS the executor, so its own store's KindUsage record (lcpp's
	// store.go AppendUsage) is the same number the engine reported, not a derived one. That
	// makes lcpp the first kind whose exactness comes from running its own engine rather than
	// reading a vendor CLI's self-reported total.
	case session.KindLcpp:
		return usagex.MeasuredExact
	case session.KindCopilot:
		return usagex.MeasuredPartial // the transcript only carries outTok
	// muse (ADR 0095 decision 10): the wire's own per-turn numbers are clean — cached input is
	// separated, the cumulative block never needs differencing, a failed turn reports nothing
	// and a resume replays none of it — so what makes this partial is not precision but
	// OWNERSHIP. Subagent and observer model calls are never folded into `session/tokenUsage`;
	// gate B1 measured one turn reporting 88,077 prompt tokens on the wire while the host's own
	// durable log recorded 116,816 across six calls, two of them owned by a subagent. AF's
	// clamps turn subagents and observers off, which makes the wire numbers complete in
	// practice — but a clamp is a setting and this is a declaration about the SOURCE, so it
	// stays partial rather than claiming an exactness the protocol does not give.
	case session.KindMuse:
		return usagex.MeasuredPartial
	}
	return usagex.MeasuredNone // kiro / cursor / agy: no tokens in the transcript
}

// foldSessionUsageLocked writes one session's unfolded portion to the ledger and returns
// the number of logical turns folded. includeTrailing=true only when the transcript is
// known not to grow any further (deletion, archiving). Requires usageFoldMu.
func foldSessionUsageLocked(m session.Meta, st *usageFoldState, includeTrailing bool) (int, bool, error) {
	if !sessionx.AgentOf(m.Kind).Caps().CanTranscript {
		return 0, false, nil // shell/ssm have no transcript
	}
	n, err := foldSessionUsageWithTurns(m, st, sessionx.UsageTurns(m), includeTrailing)
	if err != nil {
		return n, false, err
	}
	sub, subDirty, err := foldSubagentUsage(m, st, sessionx.SubagentUsageSources(m))
	return n + sub, n > 0 || subDirty, err
}

// subagentMarkKey names one subagent transcript's watermark. The session name is in it, so a
// recreated name cannot meet its predecessor's; the id is the agent's own (its path below the
// subagents directory).
func subagentMarkKey(m session.Meta, id string) string { return m.Name + "\x00" + id }

// foldSubagentUsage folds each subagent / Workflow agent transcript on its own, after the
// session's own sequence. Folding them into that sequence would renumber turns already in the
// ledger, so each agent has its own watermark (usageAgentMark) and its own Idx numbering, and
// its rows carry the agent id so the aggregation's dedup keeps one entry per agent, not per row.
// Rows stay Ref = session name and Sidechain, so the per-session figure is the one the budget
// prices (spendOfRows).
//
// A turn is recorded by identity, not by position, and with the totals written for it, so each
// fold writes exactly the difference. Nothing in an agent's file says it is finished, so the
// newest turn is folded as it stands and its growth is added later; the copies of an agent are
// merged by timestamp, so a copy found later can put an older turn first, or fill events into a
// turn already recorded; positions shift, event uuids do not. A turn is matched by its first or
// its last event id, so events added at one end do not make it a new turn. The batch is saved
// as Pending before it is appended (see usageAgentMark). An agent with no mark folds from its
// first turn, which is exact for a session that predates this change; an orphan (cost-only) row
// never occurs in a claude subagent file and is not folded here. dirty reports a change to st
// even when no row was written.
func foldSubagentUsage(m session.Meta, st *usageFoldState, srcs []sessionx.SubagentSource) (total int, dirty bool, err error) {
	for _, src := range srcs {
		key := subagentMarkKey(m, src.ID)
		mark, seen := st.Subagents[key]
		if len(mark.Pending) > 0 {
			// An earlier pass saved this batch and did not finish it.
			if err := usagex.AppendRows(mark.Pending); err != nil {
				return total, dirty, err
			}
			mark.Pending = nil
			st.Subagents[key] = mark
			dirty = true
		} else if seen && mark.Stamp == src.Stamp {
			continue // untouched since the last fold
		}
		turns, rerr := src.Turns()
		if rerr != nil {
			// A copy could not be read: what came back may lack the newest turns, and the next
			// pass reads it again. Nothing is written for this agent now.
			log.Printf("usage: fold %s subagent: %v", m.Name, rerr)
			continue
		}
		next := usageAgentMark{Seq: mark.Seq, Turns: slices.Clone(mark.Turns), Stamp: src.Stamp}
		at := map[string]int{} // event id hash -> index in next.Turns
		for i, t := range next.Turns {
			at[t.First] = i
			if t.Last != "" {
				at[t.Last] = i
			}
		}
		agent := usageAgentID(src.ID)
		var out []usagex.Record
		for _, r := range foldTurnRows(turns, true) {
			if r.Orphan {
				continue
			}
			first, last := usageAgentID(rowIdentity(r))[:12], ""
			if r.LastID != "" {
				last = usageAgentID(r.LastID)[:12]
			}
			i, known := at[first]
			if !known && last != "" {
				i, known = at[last]
			}
			var was usageAgentTurn
			if known {
				was = next.Turns[i]
			}
			delta := r
			delta.Tokens.In -= was.In
			delta.Tokens.Out -= was.Out
			delta.Tokens.CacheRead -= was.CacheRead
			delta.Tokens.CacheCreate -= was.CacheCreate
			delta.CostUSD -= was.CostUSD
			if delta.Tokens != (usagex.Tokens{}) || delta.CostUSD != 0 {
				next.Seq++
				delta.Idx = next.Seq
				out = append(out, usageRecordOf(m, delta, agent))
			}
			now := usageAgentTurn{First: first, Last: last, In: r.Tokens.In, Out: r.Tokens.Out,
				CacheRead: r.Tokens.CacheRead, CacheCreate: r.Tokens.CacheCreate, CostUSD: r.CostUSD}
			if !known {
				i = len(next.Turns)
				next.Turns = append(next.Turns, now)
			} else {
				next.Turns[i] = now
			}
			at[first] = i
			if last != "" {
				at[last] = i
			}
		}
		if len(out) == 0 {
			st.Subagents[key] = next
			dirty = true
			continue
		}
		// Save the batch before it is appended, then append, then drop it from the mark.
		saved := next
		saved.Pending = out
		st.Subagents[key] = saved
		if err := writeUsageFoldState(*st); err != nil {
			st.Subagents[key] = mark
			return total, dirty, err
		}
		if err := usagex.AppendRows(out); err != nil {
			return total, true, err // Pending stays: the next pass replays it
		}
		st.Subagents[key] = next
		total += len(out)
		dirty = true
	}
	return total, dirty, nil
}

// rowIdentity is a folded turn's id: its first event's uuid, else a stand-in from what the row
// holds (a transcript without uuids does not occur in claude's own files).
func rowIdentity(r usageTurnRow) string {
	if r.ID != "" {
		return r.ID
	}
	return fmt.Sprintf("ts:%s#%d", r.TS, r.LastIdx)
}

// usageAgentID is the id a ledger record carries for a subagent transcript: a hash, so the
// path stays out of the ledger and the indefinite dedup region.
func usageAgentID(id string) string { return usageRefKey("agent\x00" + id) }

// foldSessionUsageWithTurns is the body with the transcript load split off, so idempotency
// can be verified without a real transcript. The returned int is the number of rows
// appended to the ledger; 0 means st was not modified, so the caller need not rewrite the
// watermark.
func foldSessionUsageWithTurns(m session.Meta, st *usageFoldState, turns []transcript.Turn, includeTrailing bool) (int, error) {
	mark := st.Sessions[m.Name]
	rows := foldTurnRows(turns, includeTrailing)
	mark = rebaseCappedMark(m, mark, rows)
	fresh := unfoldedTurnRows(rows, mark)
	if len(fresh) == 0 {
		// Includes the case where the transcript shrank (archive restore, a manual edit,
		// a flip to a sibling jsonl). Never lower the watermark: lowering it counts the
		// same turns a second time.
		return 0, nil
	}
	groups := 0
	for _, r := range rows {
		if !r.Orphan {
			groups++
		}
	}
	if groups < mark.Groups {
		// Flipped to a shorter transcript. A count-only delta stopped here forever.
		log.Printf("usage: fold %s: transcript flipped from %d to %d logical turns (taking the delta by timestamp)",
			m.Name, mark.Groups, groups)
	}
	out := make([]usagex.Record, 0, len(fresh))
	for _, r := range fresh {
		out = append(out, usageRecordOf(m, r, ""))
	}
	// If the rows could not be written, do not advance the watermark. The same turns come
	// back as a delta on the next pass, so what was missed is always recovered once the
	// write works again (a partially written batch is dropped by the aggregation side's
	// (ref, idx) deduplication — usage_dedup.go). Advancing it means these turns never
	// appear as a delta again and never reach the ledger, since the ledger has no way to
	// pick up what was missed.
	if err := usagex.AppendRows(out); err != nil {
		return 0, err
	}
	// The watermark only goes up (a flip to a shorter transcript must not lower it).
	next := usageFoldMark{
		Groups:     max(mark.Groups, groups),
		Uncapped:   true,
		LastTS:     mark.LastTS,
		LastIdx:    mark.LastIdx,
		OrphanKeys: mark.OrphanKeys,
	}
	for _, r := range fresh {
		if r.Orphan {
			next.OrphanKeys = append(next.OrphanKeys, r.OrphanKey)
		} else {
			next.LastTS, next.LastIdx = laterUsageTS(next.LastTS, r.TS), r.LastIdx
		}
	}
	st.Sessions[m.Name] = next
	return len(out), nil
}

// rebaseCappedMark renumbers a claude watermark written under the capped reader. That reader
// dropped the oldest turns of a long transcript, so its Groups counted from the first visible
// turn; the uncapped reader numbers from the true first turn, k higher where k turns were
// hidden. Compared as is, the k newest turns would sit past Groups and be folded a second
// time. The transcript line of the last folded turn (LastIdx, an absolute index for claude)
// says how many turns are already behind the watermark in the new numbering. The hidden head
// was never in the ledger and stays out of it: appending it now would carry Idx values that
// collide with rows already written.
func rebaseCappedMark(m session.Meta, mark usageFoldMark, rows []usageTurnRow) usageFoldMark {
	if _, ok := sessionx.AgentOf(m.Kind).(agents.LegacyCappedUsage); !ok || mark.Uncapped || mark.Groups == 0 || mark.LastIdx == 0 {
		return mark
	}
	behind := 0
	for _, r := range rows {
		if !r.Orphan && r.LastIdx <= mark.LastIdx {
			behind++
		}
	}
	mark.Groups = max(mark.Groups, behind)
	return mark
}

// usageRecordOf is the ledger record of one folded row. agent is "" for the session's own
// transcript, else the id of the subagent transcript it came from.
func usageRecordOf(m session.Meta, r usageTurnRow, agent string) usagex.Record {
	origin, originConv := session.OriginOf(m), m.OriginConv
	rec := usagex.Record{
		TS: r.TS, Call: chatx.RandUUID(), Feature: usagex.FeatureSession, Trigger: r.Trigger,
		Origin: origin, OriginConv: originConv, Kind: m.Kind,
		Ref: m.Name, Sidechain: r.Sidechain, Idx: r.Idx, Agent: agent,
		Key: orphanLedgerKey(m, r),
		In:  r.Tokens.In, Out: r.Tokens.Out,
		CacheRead: r.Tokens.CacheRead, CacheCreate: r.Tokens.CacheCreate,
		Spend:    usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out),
		CostUSD:  r.CostUSD,
		OK:       true,
		Measured: usageMeasuredForKind(m.Kind),
	}
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if r.Model != "" {
		// The model the transcript reported is the measured value. It is a raw id
		// including the version, so it is kept in model_raw as well.
		rec.Model, rec.ModelRaw, rec.ModelSrc = r.Model, r.Model, usagex.ModelReported
	} else {
		rec.Model, rec.ModelSrc = usagex.ModelFallback(m.Model)
	}
	return rec
}

// orphanLedgerKey is a cost-only row's ledger Key ("" for a logical turn). The session name is
// part of it so the stand-in key of a message without an id cannot meet another session's.
func orphanLedgerKey(m session.Meta, r usageTurnRow) string {
	if !r.Orphan {
		return ""
	}
	return m.Kind + ":" + m.Name + ":" + r.OrphanKey
}

// unfoldedTurnRows returns the logical turns not yet folded. Deciding by both the count
// and the timestamp is the whole point here (see usageFoldMark):
//
//   - Normal case, the transcript grew by appending: everything whose sequence number is
//     past the watermark, i.e. the trailing delta.
//   - The transcript was swapped: even where sequence numbers overlap, a turn with a
//     timestamp newer than the watermark has not been counted yet, so it is taken. A turn
//     with an older timestamp is not, so another conversation's rerun is not counted.
//     Anything taken twice by mistake is absorbed by the aggregation side's (ref, idx)
//     deduplication.
//
// An Orphan row is taken by its key alone, so a session that moved to another conversation
// (a new conversation, a relaunch that could not resume) still folds the new ones.
func unfoldedTurnRows(rows []usageTurnRow, mark usageFoldMark) []usageTurnRow {
	var out []usageTurnRow
	for _, r := range rows {
		if r.Orphan {
			if !slices.Contains(mark.OrphanKeys, r.OrphanKey) {
				out = append(out, r)
			}
			continue
		}
		if r.Idx > mark.Groups || usageTSAfter(r.TS, mark.LastTS) {
			out = append(out, r)
		}
	}
	return out
}

// usageTSAfter reports whether a is after b. An unparseable timestamp answers "not after":
// treating what cannot be decided as newer piles the same turn up on every transcript read.
func usageTSAfter(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ta, err := time.Parse(time.RFC3339, a)
	if err != nil {
		return false
	}
	tb, err := time.Parse(time.RFC3339, b)
	if err != nil {
		return false
	}
	return ta.After(tb)
}

// laterUsageTS returns the newer of the two timestamps, so the watermark never rewinds.
func laterUsageTS(cur, next string) string {
	if usageTSAfter(next, cur) || cur == "" {
		return next
	}
	return cur
}

// commitSessionUsageFold closes one session's read state -> append rows -> write watermark
// inside a single critical section. Two reasons not to make the whole pass one transaction:
//
//  1. It shrinks the double-counting window to one session. A crash after the rows are
//     appended but before the watermark is written re-appends them on the next pass (the
//     rows and the watermark are separate files and cannot be written atomically). Writing
//     every session's watermark once at the end of the pass meant a crash anywhere in the
//     ~20s pass could duplicate every session folded so far. The window cannot be removed,
//     so the remaining one session's worth is dropped by the aggregation side's (ref, idx)
//     deduplication (usage_dedup.go).
//  2. It never holds the lock for long (see the usageFoldMu note above).
func commitSessionUsageFold(m session.Meta, includeTrailing bool) (int, error) {
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	n, dirty, err := foldSessionUsageLocked(m, &st, includeTrailing)
	if !dirty {
		return 0, err // st untouched, so there is nothing to rewrite
	}
	if werr := writeUsageFoldState(st); werr != nil {
		// The rows are already on disk. Silently reporting success here means nobody
		// notices when the next pass appends the same turns again.
		return n, werr
	}
	return n, err // a subagent failure after progress: what was folded is recorded
}

// foldAllSessionUsage folds every session's delta. The first run starts from watermark 0,
// so past session consumption is picked up retroactively and no dedicated backfill path is
// needed. Archived sessions are included: their transcripts are still there and the
// consumption really happened.
func foldAllSessionUsage() int {
	if !usagex.Enabled() {
		return 0
	}
	n := 0
	for _, m := range session.ListMetas() {
		// Keep folding the rest when one session fails: one broken transcript stopping
		// measurement for the whole fleet is worse. What is missed is never dropped
		// silently, it always goes to the log.
		c, err := commitSessionUsageFold(m, false)
		if err != nil {
			log.Printf("usage: fold %s: %v", m.Name, err)
		}
		n += c
	}
	return n
}

// maybeFoldSessionUsage is the fold-on-read: it runs only when usage is read, at most once
// every 60 seconds.
func maybeFoldSessionUsage() { startFoldSessionUsage(false) }

// startFoldSessionUsage starts the fold-on-read.
//
// Re-reading every session's transcript measurably takes over ten seconds (~20s for 158
// sessions). The caller (GET /sessions/usage) is already reading the same transcripts and
// is heavy, so the fold runs asynchronously and adds no response latency. The throttle and
// the running flag keep it from starting twice.
//
// force=true skips the 60 second throttle, and only when the user explicitly pressed
// refresh: pressing again right after must not be a no-op, so a turn that finished before
// the press always reaches the ledger in that one run.
//
// The return value declares that a fold is running as of this read, i.e. the value the
// caller is about to read may not include the most recent turns yet. It is the only clue
// that keeps the cost of going asynchronous from being pushed silently onto the user (who
// would otherwise press refresh repeatedly until it is current), so always put it in the
// response (folding in usage_series.go).
func startFoldSessionUsage(force bool) bool {
	if !usagex.Enabled() {
		return false
	}
	// Do not use usageFoldMu to test whether a fold is running. Doing so makes a call that
	// only asks "is it running?" wait for the fold itself to release the lock, which undoes
	// going asynchronous: the Console's usage view fires three /usage/series calls from one
	// screen, and the second and third waited in full.
	if usageFoldRunning.Load() {
		return true
	}
	usageFoldGate.Lock()
	skip := !force && !usageFoldedAt.IsZero() && time.Since(usageFoldedAt) < usageFoldPeriod
	if !skip {
		// Only the caller that wins the CAS runs: another call can have started in the
		// gap between the Load above and here.
		skip = !usageFoldRunning.CompareAndSwap(false, true)
	}
	if !skip {
		usageFoldedAt = time.Now()
	}
	usageFoldGate.Unlock()
	if skip {
		// Skipped by the throttle means nothing is running; losing the CAS means another
		// call is running.
		return usageFoldRunning.Load()
	}
	go func() {
		defer usageFoldRunning.Store(false)
		foldAllSessionUsage()
	}()
	return true
}

// finalizeSessionUsage is the settle (fold-on-delete) called just before the transcript
// disappears. It folds the open trailing turn too: the transcript will not grow any more,
// so there is no double-counting risk. Without it the last turn never reaches the ledger.
func finalizeSessionUsage(m session.Meta) {
	if !usagex.Enabled() {
		return
	}
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st := readUsageFoldState()
	if _, _, err := foldSessionUsageLocked(m, &st, true); err != nil {
		log.Printf("usage: finalize %s: %v", m.Name, err)
	}
	// The ledger has the ref baked in already, so a deleted session's watermark is not
	// kept: keeping it grows state.json monotonically with sessions that no longer exist.
	delete(st.Sessions, m.Name)
	for k := range st.Subagents {
		if strings.HasPrefix(k, m.Name+"\x00") {
			delete(st.Subagents, k)
		}
	}
	if err := writeUsageFoldState(st); err != nil {
		log.Printf("usage: finalize %s: watermark: %v", m.Name, err)
	}
}
