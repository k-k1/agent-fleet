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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

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
}

type usageFoldState struct {
	Sessions map[string]usageFoldMark `json:"sessions"`
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
	st := usageFoldState{Sessions: map[string]usageFoldMark{}}
	b, err := os.ReadFile(usageStatePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil || st.Sessions == nil {
		st.Sessions = map[string]usageFoldMark{}
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
	LastIdx   int // transcript line number of the last event (diagnostic)
}

// foldTurnRows folds a transcript's event sequence into logical turns. The aggregation
// must match aggregateUsage (session_usage.go) exactly: output tokens add up, while input
// and cache replace, because every event re-reports the whole context. A divergence here
// makes the ledger and get_session_usage disagree, i.e. two screens showing different
// numbers.
//
// With includeTrailing=false, a group that never closed is not returned (a turn still in
// progress).
func foldTurnRows(turns []transcript.Turn, includeTrailing bool) []usageTurnRow {
	var rows []usageTurnRow
	var cur usageTurnRow
	inGroup, sidechain := false, false
	trigger := usagex.TriggerUser
	fold := func() {
		if !inGroup {
			return
		}
		cur.Idx = len(rows) + 1
		rows = append(rows, cur)
		cur, inGroup = usageTurnRow{}, false
	}
	for _, t := range turns {
		if t.Role != "assistant" {
			fold()
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
			cur = usageTurnRow{Trigger: trigger, Sidechain: sidechain}
			inGroup = true
		}
		cur.Tokens.Out += t.OutTok
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
	}
	if includeTrailing {
		fold()
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
	case sessionx.TurnSourceAutoResume:
		// auto-resume after an abort (docs/log/47 §4-6) is self-repair consumption
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
	case session.KindCopilot:
		return usagex.MeasuredPartial // the transcript only carries outTok
	}
	return usagex.MeasuredNone // kiro / cursor / agy: no tokens in the transcript
}

// foldSessionUsageLocked writes one session's unfolded portion to the ledger and returns
// the number of logical turns folded. includeTrailing=true only when the transcript is
// known not to grow any further (deletion, archiving). Requires usageFoldMu.
func foldSessionUsageLocked(m session.Meta, st *usageFoldState, includeTrailing bool) (int, error) {
	if !sessionx.AgentOf(m.Kind).Caps().CanTranscript {
		return 0, nil // shell/ssm have no transcript
	}
	return foldSessionUsageWithTurns(m, st, sessionx.UsageTurns(m), includeTrailing)
}

// foldSessionUsageWithTurns is the body with the transcript load split off, so idempotency
// can be verified without a real transcript. The returned int is the number of rows
// appended to the ledger; 0 means st was not modified, so the caller need not rewrite the
// watermark.
func foldSessionUsageWithTurns(m session.Meta, st *usageFoldState, turns []transcript.Turn, includeTrailing bool) (int, error) {
	mark := st.Sessions[m.Name]
	rows := foldTurnRows(turns, includeTrailing)
	fresh := unfoldedTurnRows(rows, mark)
	if len(fresh) == 0 {
		// Includes the case where the transcript shrank (archive restore, a manual edit,
		// a flip to a sibling jsonl). Never lower the watermark: lowering it counts the
		// same turns a second time.
		return 0, nil
	}
	if len(rows) < mark.Groups {
		// Flipped to a shorter transcript. A count-only delta stopped here forever.
		log.Printf("usage: fold %s: transcript flipped from %d to %d logical turns (taking the delta by timestamp)",
			m.Name, mark.Groups, len(rows))
	}
	origin, originConv := session.OriginOf(m), m.OriginConv
	measured := usageMeasuredForKind(m.Kind)
	out := make([]usagex.Record, 0, len(fresh))
	for _, r := range fresh {
		rec := usagex.Record{
			TS: r.TS, Call: chatx.RandUUID(), Feature: usagex.FeatureSession, Trigger: r.Trigger,
			Origin: origin, OriginConv: originConv, Kind: m.Kind,
			Ref: m.Name, Sidechain: r.Sidechain, Idx: r.Idx,
			In: r.Tokens.In, Out: r.Tokens.Out,
			CacheRead: r.Tokens.CacheRead, CacheCreate: r.Tokens.CacheCreate,
			Spend:    usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out),
			OK:       true,
			Measured: measured,
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
		out = append(out, rec)
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
	last := fresh[len(fresh)-1]
	// The watermark only goes up (a flip to a shorter transcript must not lower it).
	st.Sessions[m.Name] = usageFoldMark{
		Groups:  max(mark.Groups, len(rows)),
		LastTS:  laterUsageTS(mark.LastTS, last.TS),
		LastIdx: last.LastIdx,
	}
	return len(out), nil
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
func unfoldedTurnRows(rows []usageTurnRow, mark usageFoldMark) []usageTurnRow {
	var out []usageTurnRow
	for _, r := range rows {
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
	n, err := foldSessionUsageLocked(m, &st, includeTrailing)
	if err != nil || n == 0 {
		return 0, err // n==0 leaves st untouched, so there is nothing to rewrite
	}
	if err := writeUsageFoldState(st); err != nil {
		// The rows are already on disk. Silently reporting success here means nobody
		// notices when the next pass appends the same turns again.
		return n, err
	}
	return n, nil
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
	if _, err := foldSessionUsageLocked(m, &st, true); err != nil {
		log.Printf("usage: finalize %s: %v", m.Name, err)
	}
	// The ledger has the ref baked in already, so a deleted session's watermark is not
	// kept: keeping it grows state.json monotonically with sessions that no longer exist.
	delete(st.Sessions, m.Name)
	if err := writeUsageFoldState(st); err != nil {
		log.Printf("usage: finalize %s: watermark: %v", m.Name, err)
	}
}
