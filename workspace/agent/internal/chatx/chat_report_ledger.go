package chatx

// The instruction ledger for session report v2 Phase 2 (docs/log/51 §data model /
// ADR 0035 decision 1).
//
// v1 stored "one instruction = one arm bit" (session-report/<name>.json). A bit has no
// identity, so overlapping instructions broke by construction:
//
//   - gap A: queueing instruction 2 re-armed over instruction 1's arm. Once turn 1's Stop
//     consumed that bit, instruction 2's completion was reported to nobody.
//   - gap B: the consumer (v1's waiter / kick) had no way to verify which instruction it
//     consumed (there was no generation), so consuming another instruction's share went
//     undetected.
//
// Phase 2 replaces the bit with a ledger row. One instruction = one row, and delivery always
// APPENDS (never overwrites). The row id is the identity itself, so:
//
//   - gap A disappears by construction. An earlier instruction's row turning reported leaves
//     a later instruction's row pending, and its completion is reported separately
//     (TestInstrLedgerQueuedInstructionSurvives).
//   - gap B (generation-less consumption) and misrouted hints disappear too. The reconciler
//     reports a SET OF ROW IDS and transitions only that set to reported, so which row a
//     report belongs to is always written down.
//   - Delivery can be made idempotent by row id on the sink side (under the conversation
//     lock). v1 guaranteed "exactly once" with an irreversible consume on the detection side,
//     so a failed append lost the report along with it.
//
// State machine: pending →(interim report)→ interim_reported →(completion/abnormal)→ reported
//                reported →(compensation, Phase 3)→ reopened →…→ reported
//                pending/interim_reported →(stop_session disarm)→ cancelled
// open (= a report is still owed) = pending | interim_reported | reopened.

import (
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Row states (docs/log/51 §data model).
const (
	instrPending   = "pending"
	instrInterim   = "interim_reported" // interim progress of a question/plan reported (NON-consuming)
	instrReported  = "reported"
	instrReopened  = "reopened"  // row re-opened by the compensation for a wrong "completion" (Phase 3)
	instrCancelled = "cancelled" // stop_session = the instruction is withdrawn
)

// instrCursor is the row's progress cursor: the lower bound that makes "the session worked
// after this time" a precondition of the completion report (docs/log/51 §progressed). Phase 2
// uses a single delivery time (the same meaning as Phase 1's arm time); richer per-kind cursors
// (jsonl size, turn sequence number) are later work. Holding it as SECOND-precision RFC3339 is
// the point, because the status markers it is compared against are second-precision too — going
// nano here means a fast turn that finished in the same second as the delivery never settles
// (the marker looks earlier than the cursor).
type instrCursor struct {
	At string `json:"at"` // RFC3339 (seconds)
}

// instrInterimAt records that this row already carried an interim report. The non-consuming
// semantics are identical to v1 (the completion one-shot is preserved) — this is a record of
// what was already reported, not a suppressor: two questions within one instruction is normal,
// and swallowing the second one leaves the operator unable to answer it.
type instrInterimAt struct {
	QuestionAt string `json:"question_at,omitempty"`
	PlanAt     string `json:"plan_at,omitempty"`
}

// instrRow is one instruction (docs/log/51 §data model).
type instrRow struct {
	ID          string         `json:"id"`     // row id (idempotency key for delivery)
	Conv        string         `json:"conv"`   // conversation the report goes to
	Source      string         `json:"source"` // operator | schedule | schedule-manual …
	DeliveredAt string         `json:"delivered_at"`
	Cursor      instrCursor    `json:"cursor"`
	State       string         `json:"state"`
	Interim     instrInterimAt `json:"interim,omitempty"`
	ReportedAt  string         `json:"reported_at,omitempty"`
	ReopenCount int            `json:"reopen_count,omitempty"`
}

// open reports whether the row still owes a completion report.
func (r instrRow) open() bool {
	switch r.State {
	case instrPending, instrInterim, instrReopened:
		return true
	}
	return false
}

// instrLedger is the per-session file: rows in delivery order.
type instrLedger struct {
	Rows []instrRow `json:"rows"`
}

var instrLedgers = fstore.JSON[instrLedger](paths.AgentConfigDir, "instr-ledger", ".json")

// instrClosedKeep bounds the history kept per session: open rows are always kept, closed rows
// only the newest instrClosedKeep of them — enough for reopen compensation and investigation,
// while a long-lived session steered hundreds of times does not grow the ledger.
const instrClosedKeep = 20

// Read-modify-write of a ledger is serialized per session (docs/log/51: the only writers are
// the delivery handler and the reconciler inside the server process). The critical section is
// one file read+write; delivery (conversation lock, provider calls) happens outside it.
var (
	instrLocksMu sync.Mutex
	instrLocks   = map[string]*sync.Mutex{}
)

func lockInstr(name string) func() {
	instrLocksMu.Lock()
	mu, ok := instrLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		instrLocks[name] = mu
	}
	instrLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// newInstrID mints a row id. A short id is enough (the collision surface is one session's
// ledger), but it need not be predictable either, so it is drawn from RandUUID's randomness.
func newInstrID() string {
	return "i-" + strings.ReplaceAll(RandUUID(), "-", "")[:10]
}

// ReadInstrRows returns the session's ledger rows, in delivery order.
func ReadInstrRows(name string) []instrRow {
	l, ok := instrLedgers.Read(name)
	if !ok {
		return nil
	}
	return l.Rows
}

// openInstrRows returns the rows that still owe a report, oldest cursor first — the reconciler
// depends on this order (the head = the oldest instruction = the lower bound of evidence).
func openInstrRows(name string) []instrRow {
	var out []instrRow
	for _, r := range ReadInstrRows(name) {
		if r.open() && r.Conv != "" {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Cursor.At < out[j].Cursor.At })
	return out
}

// SessionReportPending reports whether the session owes at least one report — the hook and
// record-exit processes use it to see whether a kick is worth doing at all (the successor of
// v1's reportArmed).
func SessionReportPending(name string) bool { return len(openInstrRows(name)) > 0 }

// writeInstrRows persists the rows with the retention trim. The caller must hold lockInstr.
func writeInstrRows(name string, rows []instrRow) {
	var open, closed []instrRow
	for _, r := range rows {
		if r.open() {
			open = append(open, r)
		} else {
			closed = append(closed, r)
		}
	}
	if len(closed) > instrClosedKeep {
		closed = closed[len(closed)-instrClosedKeep:]
	}
	if len(open) == 0 && len(closed) == 0 {
		instrLedgers.Remove(name)
		return
	}
	// Keep the append order (= DeliveredAt order): a closed row was not necessarily delivered
	// before an open one, so sort by time again.
	merged := append(append([]instrRow{}, closed...), open...)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].DeliveredAt < merged[j].DeliveredAt })
	_ = instrLedgers.Write(name, instrLedger{Rows: merged})
}

// AddInstruction records one delivered instruction as a NEW ledger row (docs/log/51 §migration
// Phase 2: the arm write sites become row appends). Called on success by create_session (with
// report_to) and by /input and /turn carrying report_to. NEVER touch an existing row —
// overlapping instructions not being squashed is the whole point of this replacement (gap A).
func AddInstruction(name, convID, source string) string {
	return addInstructionAt(name, convID, source, time.Now())
}

// addInstructionAt is AddInstruction with an explicit delivery time (a seam so tests can build
// the ordering between delivery and evidence deterministically).
func addInstructionAt(name, convID, source string, at time.Time) string {
	if !session.ValidName(name) || !paths.ValidIDSegment(convID) {
		return ""
	}
	if _, err := LoadConv(convID); err != nil {
		return "" // unknown conversation — no row without a destination (the same call as v1's arm)
	}
	ts := at.Format(time.RFC3339)
	row := instrRow{
		ID: newInstrID(), Conv: convID, Source: source,
		DeliveredAt: ts, Cursor: instrCursor{At: ts}, State: instrPending,
	}
	unlock := lockInstr(name)
	writeInstrRows(name, append(ReadInstrRows(name), row))
	unlock()
	// A new instruction = decide again from scratch. Carrying over the debounce (quiet count)
	// and abort hints accumulated for the previous instruction risks reading a session that has
	// not started running yet as "complete".
	reportRec.forget(name)
	return row.ID
}

// markInstrReported closes the given rows after a SUCCESSFUL delivery (deliver-then-consume).
// Rows are named by id, so instructions added between the delivery and returning here are not
// caught in the crossfire (gap B's generation-less consumption disappears structurally).
func markInstrReported(name string, ids []string, at time.Time) {
	if len(ids) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	unlock := lockInstr(name)
	defer unlock()
	rows := ReadInstrRows(name)
	for i := range rows {
		if want[rows[i].ID] && rows[i].open() {
			rows[i].State = instrReported
			rows[i].ReportedAt = at.Format(time.RFC3339)
		}
	}
	writeInstrRows(name, rows)
}

// markInstrInterim stamps the interim (non-consuming) report on every open row: a question or
// a plan is "progress on this instruction", so it is recorded as already reported on whichever
// rows are open at that moment. The state advances to interim_reported but the row STAYS open —
// the completion report is still owed.
func markInstrInterim(name, kind string, at time.Time) {
	unlock := lockInstr(name)
	defer unlock()
	rows := ReadInstrRows(name)
	ts := at.Format(time.RFC3339)
	for i := range rows {
		if !rows[i].open() {
			continue
		}
		switch kind {
		case "question":
			rows[i].Interim.QuestionAt = ts
		case "plan-approval":
			rows[i].Interim.PlanAt = ts
		default:
			continue
		}
		if rows[i].State == instrPending {
			rows[i].State = instrInterim
		}
	}
	writeInstrRows(name, rows)
}

// instrReopenMax caps how often one row may be re-opened by the compensation path
// (docs/log/51 §compensation). A row at the cap is not re-opened again: the decision is
// oscillating.
const instrReopenMax = 2

// reopenInstrRow re-opens a reported row so the real completion gets another report
// (docs/log/51 §compensation — self-healing a wrong "completion"). The detection that drives
// the transition (busy evidence returning while a reported row is under grace watch) and the
// delivery of the correction live in reportReconciler.compensate (Phase 3); this holds only the
// ledger-side transition. Call it AFTER the correction has been delivered: in the reverse order,
// a correction that could not be delivered leaves the row silently re-opened, which looks exactly
// like v1's lost report.
func reopenInstrRow(name, id string) bool {
	unlock := lockInstr(name)
	defer unlock()
	rows := ReadInstrRows(name)
	for i := range rows {
		if rows[i].ID != id || rows[i].State != instrReported {
			continue
		}
		if rows[i].ReopenCount >= instrReopenMax {
			return false // oscillating — stop instead of re-opening (Phase 3 reports it to the user)
		}
		rows[i].State = instrReopened
		rows[i].ReopenCount++
		rows[i].ReportedAt = ""
		writeInstrRows(name, rows)
		return true
	}
	return false
}

// cancelInstructions marks every open row cancelled — the operator's stop_session (halt +
// disarm_report) means "the instruction is withdrawn", so the old report is not delivered even
// if the user resumes later and it completes. The Console's stop (no body) does not call this
// and leaves the rows in place.
func cancelInstructions(name string) int {
	unlock := lockInstr(name)
	rows := ReadInstrRows(name)
	n := 0
	for i := range rows {
		if rows[i].open() {
			rows[i].State = instrCancelled
			n++
		}
	}
	writeInstrRows(name, rows)
	unlock()
	reportRec.forget(name)
	return n
}

// instrPendingSessions lists the sessions with at least one open row — the reconciler's sweep
// set. The steady-state cost is one readdir of the ledger directory plus small reads.
func instrPendingSessions() []string {
	open, _ := instrSweepSessions(time.Now())
	return open
}

// instrSweepSessions splits the ledger directory into the reconciler's two work sets in ONE
// readdir + read pass: sessions waiting for a completion (an open row) and sessions under watch
// for a wrong "completion" (a reported row inside the grace window) (docs/log/51 §compensation).
// Appearing in both is ordinary — one instruction reported while another is still pending.
func instrSweepSessions(now time.Time) (open, grace []string) {
	ents, err := os.ReadDir(instrLedgers.Dir())
	if err != nil {
		return nil, nil
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		rows := ReadInstrRows(name)
		for _, r := range rows {
			if r.open() && r.Conv != "" {
				open = append(open, name)
				break
			}
		}
		if len(instrReopenCandidates(rows, now, reportReopenGrace)) > 0 {
			grace = append(grace, name)
		}
	}
	return open, grace
}

// --- Row-set helpers (used by the reconciler and the sink) --------------------------

// instrIDs projects the row ids (ledger update targets, logging).
func instrIDs(rows []instrRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// instrDeliveryKey is the row's IDEMPOTENCY key for the conversation: row id + reopen
// generation. The generation is mixed in for the compensation (§Phase 3) — a reopened row must
// be reported AGAIN under the same row id, so keying on the row id alone reads it as "already
// delivered" and swallows the real completion report. Generation 0 (an ordinary instruction) is
// the row id itself.
func instrDeliveryKey(r instrRow) string {
	if r.ReopenCount == 0 {
		return r.ID
	}
	return r.ID + "#" + strconv.Itoa(r.ReopenCount)
}

// instrReopenKeySuffix namespaces the COMPENSATION notice's idempotency key away from the
// completion report's (docs/log/51 §compensation / Phase 3). A correction corresponds one-to-one
// with "the completion report of that generation", so its key is the same row id and the same
// generation in a separate namespace. Sharing just the row id makes the correction collide with
// the already-delivered completion report; bumping the generation instead swallows the report of
// the next real completion (the post-reopen generation).
const instrReopenKeySuffix = "~reopen"

// instrDeliveryKeyFor is the row's idempotency key for a report of the given kind.
func instrDeliveryKeyFor(kind string, r instrRow) string {
	if kind == reportKindReopened {
		return instrDeliveryKey(r) + instrReopenKeySuffix
	}
	return instrDeliveryKey(r)
}

func instrKeysFor(kind string, rows []instrRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, instrDeliveryKeyFor(kind, r))
	}
	return out
}

// instrConvs lists the distinct report targets in delivery order. Two different operator
// conversations can be instructing the same session, so folding is done PER CONVERSATION
// (mixing them into one message leaks one conversation's instruction completion to the other).
func instrConvs(rows []instrRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.Conv != "" && !seen[r.Conv] {
			seen[r.Conv] = true
			out = append(out, r.Conv)
		}
	}
	return out
}

func instrRowsForConv(rows []instrRow, conv string) []instrRow {
	var out []instrRow
	for _, r := range rows {
		if r.Conv == conv {
			out = append(out, r)
		}
	}
	return out
}

// instrRowsCoveredBy returns the rows whose cursor is not AFTER the settle evidence (at = the
// idle marker / ExitInfo time). An instruction delivered after the evidence cannot have
// completed in that quiet period — it stays open and is reported at the next terminus (gap A).
// When the time cannot be parsed reportTimeBefore returns false, which falls to "covered":
// discarding real completion evidence because the decision is undecidable means the report never
// comes out at all (v1's loss mode).
func instrRowsCoveredBy(rows []instrRow, at string) []instrRow {
	var out []instrRow
	for _, r := range rows {
		if !reportTimeBefore(at, r.Cursor.At) {
			out = append(out, r)
		}
	}
	return out
}

// undeliveredInstrRows filters out the rows whose report of THIS kind already exists in the
// conversation (delivery idempotency — the caller must hold the conversation lock). It takes a
// kind for the compensation's correction (docs/log/51 §compensation): the correction and the
// completion report name the same row and the same generation but are two separate messages, so
// without separate key namespaces one swallows the other.
func undeliveredInstrRows(c *ChatConversation, rows []instrRow, kind string) []instrRow {
	if len(rows) == 0 {
		return rows
	}
	done := map[string]bool{}
	for i := range c.Messages {
		if c.Messages[i].Role != "report" {
			continue
		}
		for _, k := range c.Messages[i].Instr {
			done[k] = true
		}
	}
	var out []instrRow
	for _, r := range rows {
		if !done[instrDeliveryKeyFor(kind, r)] {
			out = append(out, r)
		}
	}
	return out
}

// reportedInstrTS finds when the conversation actually carried these rows' completion report
// (unix millis, 0 when not found). NOT using the ledger's ReportedAt is the reason this function
// exists: the compensation clears ReportedAt the moment it reopens a row (reopenInstrRow), so
// reading "which report is being corrected" from the ledger finds the reference gone on a
// retried second pass or a second compensation. The conversation messages are the thing being
// corrected, so taking it from there keeps the generation aligned.
func reportedInstrTS(c *ChatConversation, rows []instrRow) int64 {
	want := map[string]bool{}
	for _, r := range rows {
		want[instrDeliveryKey(r)] = true
	}
	var ts int64
	for i := range c.Messages {
		if c.Messages[i].Role != "report" {
			continue
		}
		for _, k := range c.Messages[i].Instr {
			if want[k] && (ts == 0 || c.Messages[i].TS < ts) {
				ts = c.Messages[i].TS
			}
		}
	}
	return ts
}

// instrReopenCandidates returns the reported rows still inside the compensation grace window
// (docs/log/51 §compensation: reported rows are watched for the grace period). It is a pure
// function, so the grace boundary and the "no compensation once a new instruction arrived" rule
// can be pinned by table.
//
// The "with NO newer instruction row" part is implemented here: if the session going busy again
// is explained by an instruction delivered after that report, it is the next job rather than a
// wrong report, so it is excluded from compensation (otherwise every queued instruction would
// correct the preceding, correct report).
func instrReopenCandidates(rows []instrRow, now time.Time, grace time.Duration) []instrRow {
	newest := ""
	for _, r := range rows {
		if r.DeliveredAt > newest {
			newest = r.DeliveredAt
		}
	}
	var out []instrRow
	for _, r := range rows {
		if r.State != instrReported || r.ReportedAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.ReportedAt)
		if err != nil {
			continue // a row with an unparsable time cannot be watched (no start for the grace)
		}
		if now.Before(at) || now.Sub(at) > grace {
			continue
		}
		if reportTimeBefore(r.ReportedAt, newest) {
			continue // a new instruction arrived after the report — the busy state is explained
		}
		out = append(out, r)
	}
	return out
}

// instrFoldAts joins the dispatch times of the rows a folded report covers (docs/log/51 §data
// model): when several instructions complete in the same quiet period they are bundled into one
// message EXPLICITLY, not squashed as in v1. The "N instructions" wording itself lives in
// chat_report_text.go (composed in the display language).
func instrFoldAts(rows []instrRow) string {
	ats := make([]string, 0, len(rows))
	for _, r := range rows {
		ats = append(ats, r.DeliveredAt)
	}
	return strings.Join(ats, " / ")
}

// --- Migration from the v1 arm ------------------------------------------------------

// MigrateReportArms converts leftover v1 arm files (session-report/<name>.json, armed=true)
// into one ledger row each, then removes them (docs/log/51 §migration Phase 2: "convert an
// existing armed=true into one row at startup"). Runs once at startup.
//
// The v1 files are deleted after conversion so a restart does not re-convert the same arm and
// multiply rows. The price is that rolling back to a Phase 1 binary drops the report for a
// migrated but still-open instruction (re-issuing the instruction recovers it) — chosen as the
// lighter failure against double reporting.
func MigrateReportArms() {
	ents, err := os.ReadDir(reportLinks.Dir())
	if err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		l, ok := reportLinks.Read(name)
		if !ok {
			continue
		}
		if l.Armed && l.Conv != "" && !SessionReportPending(name) {
			at := l.At
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				at = time.Now().Format(time.RFC3339)
			}
			unlock := lockInstr(name)
			writeInstrRows(name, append(ReadInstrRows(name), instrRow{
				ID: newInstrID(), Conv: l.Conv, Source: "v1-arm",
				DeliveredAt: at, Cursor: instrCursor{At: at}, State: instrPending,
			}))
			unlock()
			n++
		}
		reportLinks.Remove(name)
	}
	if n > 0 {
		log.Printf("session-report: migrated %d v1 arm(s) into the instruction ledger (docs/log/51 Phase 2)", n)
	}
}
