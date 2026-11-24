package main

// Tidying before a fire (ADR 0137 section 8): apply, undo and the run list. Apply is the one write
// that turns a validated reply into messages, and the automatic run and the manual Tidy both call
// it. Nothing in this file starts a run or calls a model.
//
// What a run may do is decided twice and by different code: control-plane/internal/memotidy says
// whether a reply is acceptable, and apply re-reads every message inside the member's lock, so that
// what it changes is what the run read. The tidy switch is a guard rail, not a wall: the reply is
// produced inside the workspace, so code there that can answer for the Agent chooses the text of a
// derived message within memotidy's bounds. The bounds, the group rule and the undo are what
// contain that.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memoTidyRunsListed is how many runs the run list answers.
const memoTidyRunsListed = 20

// Reasons a run ends without a change that no memotidy refusal names.
const (
	memoTidyReasonStaleInput = "stale_input"
	memoTidyReasonNoQueue    = "no_queue"
)

// Why an undo is refused for one message.
const (
	memoTidyBlockClaimed      = "claimed"
	memoTidyBlockSent         = "sent"
	memoTidyBlockChanged      = "changed"
	memoTidyBlockGone         = "gone"
	memoTidyBlockLaterRun     = "later_run"
	memoTidyBlockDuplicateRef = "duplicate_ref"
)

// errMemoTidyNotRunning is returned by apply for a run that is not running any more, a late apply
// after the run was taken over. Nothing was written.
var errMemoTidyNotRunning = errors.New("the tidy run is not running")

// memoTidyItem is a message as a run read it: its content hash and its state.
type memoTidyItem struct {
	ID    string `json:"id"`
	Hash  string `json:"hash"`
	State string `json:"state"`
	// PrevRun is the run that made the row before this one superseded it, so that an undo of this
	// run gives the row back to that run's undo. It is set on a run's sources only.
	PrevRun string `json:"prevRun,omitempty"`
}

// memoTidyInput is what a run's input column holds: ids, hashes and states, never text.
type memoTidyInput struct {
	Items []memoTidyItem `json:"items"`
}

// memoTidyMove records a moved message, with what an undo needs to put it back.
type memoTidyMove struct {
	ID string `json:"id"`
	// State, ReleaseHash, ReleasedAt and ReleasedBy are the message's before the move.
	State       string `json:"state"`
	PrevRun     string `json:"prevRun,omitempty"`
	ReleaseHash string `json:"releaseHash,omitempty"`
	ReleasedAt  string `json:"releasedAt,omitempty"`
	ReleasedBy  string `json:"releasedBy,omitempty"`
	// Hash and ToState are what the run wrote.
	Hash    string `json:"hash"`
	ToState string `json:"toState"`
	// Recipe is the run recipe the message had before the move: a move out of every queue drops it,
	// and the undo puts it back.
	Recipe string `json:"recipe,omitempty"`
}

// memoTidyRecord is what an applied run's ops column holds.
type memoTidyRecord struct {
	Merged  int            `json:"merged"`
	Split   int            `json:"split"`
	Moved   int            `json:"moved"`
	Held    int            `json:"held"`
	Sources []memoTidyItem `json:"sources"`
	Results []memoTidyItem `json:"results"`
	Moves   []memoTidyMove `json:"moves"`
}

// memoTidyApplyIn is what a run hands apply.
type memoTidyApplyIn struct {
	MembershipID string
	// QueueID is the category id of the queue that was tidied.
	QueueID string
	// RunID is a run reserved as running. Empty creates one inside apply's transaction, which is
	// how the manual Tidy calls it: a run that is never visible as running.
	RunID   string
	Trigger string
	// Input is every message the run read, as it read it. An automatic run takes only pending
	// messages, the manual one pending or held.
	Input []memoTidyItem
	// Destinations are the category ids the model was offered, the queue itself left out.
	Destinations []string
	Ops          []memotidy.Op
}

// memoTidyApplyOut says what apply did. Run.State is applied, kept or dropped.
type memoTidyApplyOut struct {
	Run                     store.MemoTidyRun
	Sources, Results, Moved []string
}

// memoTidyApply validates the operations against the messages as they are now and applies them, all
// or none, in one MemoTx. Every message of the input must still be what the run read: in the queue,
// not sent, not claimed, not in a flush, holding the recorded hash and state. Otherwise nothing is
// applied and the run is dropped with reason stale_input. A refused reply drops the run with the
// refusal's code. Whatever the outcome, every message of the input is marked seen.
//
// A source of a merge or a split is superseded and a derived message takes its place, in the state
// of its sources. A move goes where the queue's stored settings say, read here and never from the
// reply. A returned error means nothing was written, errMemoTidyNotRunning for a run that was taken
// over meanwhile.
func memoTidyApply(ctx context.Context, st store.MemoQueueStore, in memoTidyApplyIn) (memoTidyApplyOut, error) {
	var out memoTidyApplyOut
	err := st.MemoTx(ctx, in.MembershipID, func(tx store.MemoTxOps) error {
		out = memoTidyApplyOut{}
		return memoTidyApplyTx(tx, in, &out)
	})
	if err != nil {
		return memoTidyApplyOut{}, err
	}
	return out, nil
}

func memoTidyApplyTx(tx store.MemoTxOps, in memoTidyApplyIn, out *memoTidyApplyOut) error {
	now := memoNow()
	var run store.MemoTidyRun
	if in.RunID != "" {
		r, ok, err := tx.GetTidyRun(in.RunID)
		if err != nil {
			return err
		}
		if !ok || r.State != store.TidyRunRunning || r.QueueID != in.QueueID {
			return errMemoTidyNotRunning
		}
		run = r
	} else {
		input, _ := json.Marshal(memoTidyInput{Items: in.Input})
		run = store.MemoTidyRun{ID: store.NewID(), MembershipID: in.MembershipID, QueueID: in.QueueID, Trigger: in.Trigger,
			State: store.TidyRunRunning, Input: string(input), CreatedAt: now, UpdatedAt: now}
		if err := tx.InsertTidyRun(run); err != nil {
			return err
		}
	}
	finish := func(state, reason string, rec *memoTidyRecord) error {
		for _, it := range in.Input {
			if err := tx.MarkTidySeen(it.ID, it.Hash); err != nil {
				return err
			}
		}
		run.State, run.Reason, run.FinishedAt, run.UpdatedAt = state, reason, now, now
		if rec != nil {
			b, _ := json.Marshal(rec)
			run.Ops = string(b)
		}
		ok, err := tx.FinishTidyRun(run)
		if err != nil {
			return err
		}
		if !ok {
			return errMemoTidyNotRunning
		}
		out.Run = run
		return nil
	}

	cat, ok, err := tx.GetCategory(in.QueueID)
	if err != nil {
		return err
	}
	q, isQueue, err := tx.GetQueue(in.QueueID)
	if err != nil {
		return err
	}
	if !ok || !isQueue {
		return finish(store.TidyRunDropped, memoTidyReasonNoQueue, nil)
	}

	// Every source must still be what the run read.
	rows := make(map[string]store.Memo, len(in.Input))
	for _, it := range in.Input {
		m, found, err := tx.GetMemo(it.ID)
		if err != nil {
			return err
		}
		_, dup := rows[it.ID]
		stateOK := it.State == store.MemoStatePending || (it.State == store.MemoStateHeld && in.Trigger == store.TidyTriggerManual)
		if !found || dup || !stateOK || m.State != it.State || m.Repo != cat.Repo || m.Category != cat.Name ||
			m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" || memoContentHash(m) != it.Hash {
			return finish(store.TidyRunDropped, memoTidyReasonStaleInput, nil)
		}
		rows[it.ID] = m
	}
	if len(in.Input) > memotidy.MaxInputItems || len(in.Destinations) > memotidy.MaxDestinations {
		return finish(store.TidyRunDropped, memotidy.CodeBadShape, nil)
	}

	// The destinations as they are now: only those a move names need their open references.
	named := map[string]bool{}
	for _, op := range in.Ops {
		if op.Op == memotidy.OpMove {
			named[op.To] = true
		}
	}
	cats := map[string]store.MemoCategory{}
	var dests []memotidy.Destination
	for _, id := range in.Destinations {
		c, found, err := tx.GetCategory(id)
		if err != nil {
			return err
		}
		if !found || id == in.QueueID {
			continue
		}
		cats[id] = c
		d := memotidy.Destination{ID: id}
		if named[id] {
			held, err := tx.ListMemosIn(c.Repo, c.Name)
			if err != nil {
				return err
			}
			d.OpenRefs = map[string]bool{}
			for _, h := range held {
				if memoOpenState(h.State) && memoRefIdent(h) != "" {
					d.OpenRefs[memoRefIdent(h)] = true
				}
			}
		}
		dests = append(dests, d)
	}
	bucket, err := tx.ListMemosIn(cat.Repo, cat.Name)
	if err != nil {
		return err
	}
	linked, err := memoLinked(tx, bucket)
	if err != nil {
		return err
	}
	items := make([]memotidy.Item, 0, len(in.Input))
	for _, it := range in.Input {
		item := memoTidyItemOf(rows[it.ID])
		item.KeepOnly = linked[it.ID]
		items = append(items, item)
	}
	if r := memotidy.Validate(memotidy.Request{Self: in.QueueID, Items: items, Destinations: dests, Ops: in.Ops}); r != nil {
		return finish(store.TidyRunDropped, r.Code, nil)
	}

	rec := memoTidyRecord{Sources: []memoTidyItem{}, Results: []memoTidyItem{}, Moves: []memoTidyMove{}}
	supersede := func(m store.Memo) error {
		rec.Sources = append(rec.Sources, memoTidyItem{ID: m.ID, Hash: memoContentHash(m), State: m.State, PrevRun: m.TidyRunID})
		out.Sources = append(out.Sources, m.ID)
		m.State, m.SentAt, m.TidyRunID = store.MemoStateSuperseded, store.NowTS(), run.ID
		return tx.WriteTidyMemo(m)
	}
	derive := func(sources []store.Memo, text string, position int, group memotidy.Group) error {
		state := store.MemoStatePending
		ids := make([]string, 0, len(sources))
		created := sources[0].CreatedAt
		for _, s := range sources {
			ids = append(ids, s.ID)
			if s.State != store.MemoStatePending {
				state = store.MemoStateHeld
			}
			if s.CreatedAt < created {
				created = s.CreatedAt
			}
		}
		d := store.Memo{ID: store.NewID(), MembershipID: in.MembershipID, Repo: cat.Repo, Category: cat.Name, Kind: "text", Body: text,
			Position: position, CreatedAt: created, State: state, Provenance: store.MemoProvenanceCurator,
			SourceProvenance: group.Provenance, Producer: group.Producer, ProducerOrigin: group.ProducerOrigin,
			DerivedFrom: encodeIDs(ids), TidyRunID: run.ID, Recipe: sources[0].Recipe}
		h := memoContentHash(d)
		d.TidySeenHash = h
		if state == store.MemoStatePending {
			d.ReleaseHash, d.ReleasedAt, d.ReleasedBy = h, now, store.MemoReleasedByTidy
		}
		if err := tx.InsertMemo(d); err != nil {
			return err
		}
		rec.Results = append(rec.Results, memoTidyItem{ID: d.ID, Hash: h, State: state})
		out.Results = append(out.Results, d.ID)
		return nil
	}
	for _, op := range in.Ops {
		switch op.Op {
		case memotidy.OpMerge:
			srcs := make([]store.Memo, 0, len(op.IDs))
			pos := rows[op.IDs[0]].Position
			for _, id := range op.IDs {
				srcs = append(srcs, rows[id])
				pos = min(pos, rows[id].Position)
			}
			if err := derive(srcs, memotidy.CleanText(op.Text), pos, memoTidyItemOf(srcs[0]).Group); err != nil {
				return err
			}
			for _, s := range srcs {
				if err := supersede(s); err != nil {
					return err
				}
			}
			rec.Merged++
		case memotidy.OpSplit:
			src := rows[op.ID]
			for _, t := range op.Texts {
				if err := derive([]store.Memo{src}, memotidy.CleanText(t), src.Position, memoTidyItemOf(src).Group); err != nil {
					return err
				}
			}
			if err := supersede(src); err != nil {
				return err
			}
			rec.Split++
		case memotidy.OpMove:
			m := rows[op.ID]
			mv, err := memoTidyMoveOne(tx, q, m, cats[op.To], run.ID, in.QueueID)
			if err != nil {
				return err
			}
			rec.Moves = append(rec.Moves, mv)
			out.Moved = append(out.Moved, m.ID)
			rec.Moved++
			if mv.ToState == store.MemoStateHeld {
				rec.Held++
			}
		}
	}
	if len(rec.Sources) == 0 && len(rec.Moves) == 0 {
		return finish(store.TidyRunKept, "", nil)
	}
	return finish(store.TidyRunApplied, "", &rec)
}

// memoTidyMoveOne moves m into dest and returns the record of it. Where it lands is read from the
// queue's stored settings: a plain category makes it a plain note, a queue the settings list takes
// a pending message as pending, and any other queue holds it for the person to release there.
// A held message stays held, so a move never releases one.
func memoTidyMoveOne(tx store.MemoTxOps, q store.MemoQueue, m store.Memo, dest store.MemoCategory, runID, queueID string) (memoTidyMove, error) {
	rec := memoTidyMove{ID: m.ID, State: m.State, PrevRun: m.TidyRunID, ReleaseHash: m.ReleaseHash, ReleasedAt: m.ReleasedAt, ReleasedBy: m.ReleasedBy, Recipe: m.Recipe}
	_, destIsQueue, err := tx.GetQueue(dest.ID)
	if err != nil {
		return rec, err
	}
	rows, err := tx.ListMemosIn(dest.Repo, dest.Name)
	if err != nil {
		return rec, err
	}
	next := m
	next.Repo, next.Category, next.Position = dest.Repo, dest.Name, 0
	for _, r := range rows {
		next.Position = max(next.Position, r.Position+1)
	}
	next.TidyFrom, next.TidyRunID = queueID, runID
	switch {
	case !destIsQueue:
		next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = "", "", "", ""
		next.Recipe = ""
	case m.State == store.MemoStatePending && slices.Contains(q.TidyDestinations, dest.ID):
		next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = store.MemoStatePending, memoContentHash(next), memoNow(), store.MemoReleasedByTidy
	default:
		next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = store.MemoStateHeld, "", "", ""
	}
	rec.Hash, rec.ToState = memoContentHash(next), next.State
	if err := tx.WriteTidyMemo(next); err != nil {
		return rec, err
	}
	// A move leaves the queue whose order the member's run-next mark was a place in (memoUpdateFor).
	if m.RunNext {
		if _, err := tx.SetMemoRunNext(m.ID, false); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// memoTidyItemOf is a message as the validator sees it. A derived message is grouped by the
// provenance of its sources, so that tidying never mixes what a member wrote with what a session
// wrote, however many times it has been tidied.
func memoTidyItemOf(m store.Memo) memotidy.Item {
	prov := memoEffectiveProvenance(m)
	if m.Provenance == store.MemoProvenanceCurator && m.SourceProvenance != "" {
		prov = m.SourceProvenance
	}
	return memotidy.Item{ID: m.ID, Group: memotidy.Group{Provenance: prov, Producer: m.Producer, ProducerOrigin: m.ProducerOrigin, Recipe: m.Recipe},
		Mergeable: m.Kind == "text" && m.Body != "" && m.Attachments == "" && m.RefPath == "" && m.RefKey == "",
		Bytes:     len(m.Body), Moved: m.TidyFrom != "", Ref: memoRefIdent(m)}
}

func encodeIDs(ids []string) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

func decodeIDs(s string) []string {
	var out []string
	if s == "" || json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// memoTidyDailyUsed is how many automatic runs a queue reserved in the last 24 hours, every outcome
// counted. Manual runs are not counted.
func memoTidyDailyUsed(ctx context.Context, st store.MemoTidyStore, queueID string, now time.Time) (int, error) {
	return st.CountMemoTidyRunsSince(ctx, queueID, now.UTC().Add(-24*time.Hour).Format(time.RFC3339Nano))
}

// --- Undo -------------------------------------------------------------------------------------

// memoTidyBlock is one message that keeps a run from being undone.
type memoTidyBlock struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

// memoTidyUndoPlan is what undoing a run writes: the rows to put back and the derived ones to
// remove. It is computed from reads only, so the run list shows the same verdict an undo reaches.
type memoTidyUndoPlan struct {
	restore []store.Memo
	remove  []string
}

// memoTidyUndoCheck decides whether run can be undone and what undoing it writes. Every message
// that stops it is listed. A result or a moved message must still be what the run wrote and be
// untouched by a delivery or a later run, and every source must still be superseded by this run.
func memoTidyUndoCheck(rd store.MemoTidyReader, run store.MemoTidyRun, rec memoTidyRecord) (memoTidyUndoPlan, []memoTidyBlock, error) {
	var plan memoTidyUndoPlan
	blocked := []memoTidyBlock{}
	block := func(id, why string) { blocked = append(blocked, memoTidyBlock{ID: id, Why: why}) }
	// untouched says why a message a run wrote can no longer be taken back, "" when it can.
	untouched := func(m store.Memo, want memoTidyItem) string {
		switch {
		case m.TidyRunID != run.ID:
			return memoTidyBlockLaterRun
		case m.State == store.MemoStateClaimed || m.DeliveryID != "" || m.SendToken != "":
			return memoTidyBlockClaimed
		case m.State == store.MemoStateDelivered || m.State == store.MemoStateUnconfirmed || m.SentAt != "":
			return memoTidyBlockSent
		case m.State != want.State || memoContentHash(m) != want.Hash:
			return memoTidyBlockChanged
		}
		return ""
	}

	for _, r := range rec.Results {
		m, ok, err := rd.GetMemo(r.ID)
		if err != nil {
			return plan, nil, err
		}
		if !ok {
			block(r.ID, memoTidyBlockGone)
		} else if why := untouched(m, r); why != "" {
			block(r.ID, why)
		}
		plan.remove = append(plan.remove, r.ID)
	}

	for _, s := range rec.Sources {
		m, ok, err := rd.GetMemo(s.ID)
		if err != nil {
			return plan, nil, err
		}
		if !ok {
			block(s.ID, memoTidyBlockGone)
			continue
		}
		if m.State != store.MemoStateSuperseded || m.TidyRunID != run.ID {
			block(s.ID, memoTidyBlockChanged)
			continue
		}
		_, inQ, err := rd.QueueOf(m.Repo, m.Category)
		if err != nil {
			return plan, nil, err
		}
		next := m
		next.SentAt, next.TidyRunID = "", s.PrevRun
		switch {
		case s.State == store.MemoStatePending && inQ && m.ReleaseHash != "" && m.ReleaseHash == memoContentHash(m):
			next.State = store.MemoStatePending
		case inQ:
			next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = store.MemoStateHeld, "", "", ""
		default:
			next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = "", "", "", ""
		}
		next.TidySeenHash = memoContentHash(next)
		plan.restore = append(plan.restore, next)
	}

	// A moved message goes back to the end of the queue it came from.
	var home store.MemoCategory
	var homeIsQueue bool
	var homeRows []store.Memo
	if len(rec.Moves) > 0 {
		c, ok, err := rd.GetCategory(run.QueueID)
		if err != nil {
			return plan, nil, err
		}
		if ok {
			home = c
			_, homeIsQueue, err = rd.GetQueue(c.ID)
			if err != nil {
				return plan, nil, err
			}
			if homeRows, err = rd.ListMemosIn(c.Repo, c.Name); err != nil {
				return plan, nil, err
			}
		}
	}
	removed := map[string]bool{}
	for _, id := range plan.remove {
		removed[id] = true
	}
	next := 0
	for _, r := range homeRows {
		next = max(next, r.Position+1)
	}
	for _, mv := range rec.Moves {
		m, ok, err := rd.GetMemo(mv.ID)
		if err != nil {
			return plan, nil, err
		}
		if !ok || home.ID == "" {
			block(mv.ID, memoTidyBlockGone)
			continue
		}
		if why := untouched(m, memoTidyItem{ID: mv.ID, Hash: mv.Hash, State: mv.ToState}); why != "" {
			block(mv.ID, why)
			continue
		}
		back := m
		back.Repo, back.Category, back.Position = home.Repo, home.Name, next
		back.TidyFrom, back.TidyRunID = "", mv.PrevRun
		// Only a queue keeps a recipe: the row may have moved on into another queue since, and a plain
		// note carries none.
		back.Recipe = ""
		if homeIsQueue {
			back.Recipe = mv.Recipe
		}
		switch {
		case mv.State == store.MemoStatePending && homeIsQueue && mv.ReleaseHash == memoContentHash(back):
			back.State, back.ReleaseHash, back.ReleasedAt, back.ReleasedBy = store.MemoStatePending, mv.ReleaseHash, mv.ReleasedAt, mv.ReleasedBy
		case homeIsQueue:
			back.State, back.ReleaseHash, back.ReleasedAt, back.ReleasedBy = store.MemoStateHeld, "", "", ""
		default:
			back.State, back.ReleaseHash, back.ReleasedAt, back.ReleasedBy = "", "", "", ""
		}
		if homeIsQueue && memoOpenState(back.State) {
			var open []store.Memo
			for _, r := range homeRows {
				if !removed[r.ID] {
					open = append(open, r)
				}
			}
			if dup, found := memoOpenRefIn(open, back); found && dup.ID != back.ID {
				block(mv.ID, memoTidyBlockDuplicateRef)
				continue
			}
		}
		back.TidySeenHash = memoContentHash(back)
		next++
		plan.restore = append(plan.restore, back)
	}
	return plan, blocked, nil
}

// memoTidyUndoOut is an undone run: the messages put back and the derived ones removed.
type memoTidyUndoOut struct {
	Run      store.MemoTidyRun
	Restored []store.Memo
	Removed  []string
}

// memoTidyNotUndoable is the refusal of an undo. Blocked names every message that stops it, and is
// empty when the run itself is not an applied one.
type memoTidyNotUndoable struct {
	Blocked []memoTidyBlock
	Message string
}

func (e memoTidyNotUndoable) Error() string { return e.Message }

// memoTidyUndo puts a run back, all or none, in one MemoTx: the derived messages are deleted, each
// source and each moved message returns to the state it had, and the run becomes undone. It answers
// memoTidyNotUndoable when any message stops it.
func memoTidyUndo(ctx context.Context, st store.MemoQueueStore, membershipID, runID string) (memoTidyUndoOut, error) {
	var out memoTidyUndoOut
	err := st.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		out = memoTidyUndoOut{}
		run, ok, err := tx.GetTidyRun(runID)
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "tidy run not found"})
		}
		if run.State != store.TidyRunApplied {
			return memoTidyNotUndoable{Blocked: []memoTidyBlock{}, Message: "only a run that changed something can be undone, and this one is " + run.State}
		}
		var rec memoTidyRecord
		if err := json.Unmarshal([]byte(run.Ops), &rec); err != nil {
			return err
		}
		plan, blocked, err := memoTidyUndoCheck(tx, run, rec)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			return memoTidyNotUndoable{Blocked: blocked, Message: "a message of this run was changed, sent or tidied again since"}
		}
		for _, id := range plan.remove {
			if err := tx.DeleteMemo(id); err != nil {
				return err
			}
		}
		for _, m := range plan.restore {
			if err := tx.RestoreMemo(m); err != nil {
				return err
			}
		}
		at := memoNow()
		if won, err := tx.UndoTidyRun(run.ID, at); err != nil {
			return err
		} else if !won {
			return memoTidyNotUndoable{Blocked: []memoTidyBlock{}, Message: "this run was undone already"}
		}
		run.State, run.UndoneAt, run.UpdatedAt = store.TidyRunUndone, at, at
		out = memoTidyUndoOut{Run: run, Restored: plan.restore, Removed: plan.remove}
		return nil
	})
	if err != nil {
		return memoTidyUndoOut{}, err
	}
	return out, nil
}

// --- Wire -------------------------------------------------------------------------------------

// memoTidyRunDTO is a run on the wire. Counts and ids are those of an applied or undone run and
// empty for any other. Undoable is true for an applied run that nothing blocks, and Blocked names
// the messages that do.
type memoTidyRunDTO struct {
	ID         string          `json:"id"`
	Trigger    string          `json:"trigger"`
	State      string          `json:"state"`
	Reason     string          `json:"reason"`
	CreatedAt  string          `json:"createdAt"`
	FinishedAt string          `json:"finishedAt"`
	UndoneAt   string          `json:"undoneAt"`
	Merged     int             `json:"merged"`
	Split      int             `json:"split"`
	Moved      int             `json:"moved"`
	Held       int             `json:"held"`
	SourceIDs  []string        `json:"sourceIds"`
	ResultIDs  []string        `json:"resultIds"`
	MovedIDs   []string        `json:"movedIds"`
	Undoable   bool            `json:"undoable"`
	Blocked    []memoTidyBlock `json:"blocked"`
}

func memoTidyRunToDTO(r store.MemoTidyRun, rec memoTidyRecord) memoTidyRunDTO {
	d := memoTidyRunDTO{ID: r.ID, Trigger: r.Trigger, State: r.State, Reason: r.Reason, CreatedAt: r.CreatedAt,
		FinishedAt: r.FinishedAt, UndoneAt: r.UndoneAt, Merged: rec.Merged, Split: rec.Split, Moved: rec.Moved, Held: rec.Held,
		SourceIDs: []string{}, ResultIDs: []string{}, MovedIDs: []string{}, Blocked: []memoTidyBlock{}}
	for _, s := range rec.Sources {
		d.SourceIDs = append(d.SourceIDs, s.ID)
	}
	for _, s := range rec.Results {
		d.ResultIDs = append(d.ResultIDs, s.ID)
	}
	for _, m := range rec.Moves {
		d.MovedIDs = append(d.MovedIDs, m.ID)
	}
	return d
}

// memoTidyRunToCheckedDTO is memoTidyRunToDTO plus the undo verdict (Undoable, Blocked) that only an
// applied run has. The run list and the apply answer both go through it, so they cannot disagree; rd must read committed state, after the run's own transaction.
func memoTidyRunToCheckedDTO(rd store.MemoTidyReader, run store.MemoTidyRun, rec memoTidyRecord) (memoTidyRunDTO, error) {
	d := memoTidyRunToDTO(run, rec)
	if run.State == store.TidyRunApplied {
		_, blocked, err := memoTidyUndoCheck(rd, run, rec)
		if err != nil {
			return d, err
		}
		d.Blocked, d.Undoable = blocked, len(blocked) == 0
	}
	return d, nil
}

// memoTidyRunsWire is GET /api/memo-queues/{categoryId}/tidy-runs.
type memoTidyRunsWire struct {
	Runs      []memoTidyRunDTO `json:"runs"`
	UsedToday int              `json:"usedToday"`
	DailyCap  int              `json:"dailyCap"`
}

// listTidyRuns answers a queue's newest runs. The category must be the member's.
func (a memoAPI) listTidyRuns(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	ctx := r.Context()
	c, ok, err := a.store.GetCategory(ctx, r.PathValue("categoryId"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || c.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category not found"})
		return
	}
	runs, err := a.store.ListMemoTidyRuns(ctx, mv.MembershipID, c.ID, memoTidyRunsListed)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	used, err := memoTidyDailyUsed(ctx, a.store, c.ID, time.Now())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := memoTidyRunsWire{Runs: make([]memoTidyRunDTO, 0, len(runs)), UsedToday: used, DailyCap: memotidy.DailyCap}
	err = a.store.MemoTidyRead(ctx, mv.MembershipID, func(rd store.MemoTidyReader) error {
		for _, run := range runs {
			var rec memoTidyRecord
			if run.Ops != "" {
				_ = json.Unmarshal([]byte(run.Ops), &rec)
			}
			d, err := memoTidyRunToCheckedDTO(rd, run, rec)
			if err != nil {
				return err
			}
			out.Runs = append(out.Runs, d)
		}
		return nil
	})
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// memoTidyUndoWire is a run undone: the run, the messages put back and the ids removed.
type memoTidyUndoWire struct {
	Run      memoTidyRunDTO `json:"run"`
	Restored []memoDTO      `json:"restored"`
	Removed  []string       `json:"removed"`
}

// memoTidyNotUndoableWire is the 409 of an undo: the usual error envelope plus the messages that
// stop it, so that the Console can say which.
type memoTidyNotUndoableWire struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Blocked []memoTidyBlock `json:"blocked"`
	} `json:"error"`
}

// undoTidy (POST /api/memo-tidy-runs/{runId}/undo) puts a run back. Console only: no route of the
// token face or /mcp reaches it.
func (a memoAPI) undoTidy(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	out, err := memoTidyUndo(r.Context(), a.store, mv.MembershipID, r.PathValue("runId"))
	var nu memoTidyNotUndoable
	if errors.As(err, &nu) {
		var body memoTidyNotUndoableWire
		body.Error.Code, body.Error.Message, body.Error.Blocked = errCodeMemoTidyNotUndoable, nu.Message, nu.Blocked
		writeJSON(w, http.StatusConflict, body)
		return
	}
	if err != nil {
		writeAPIErr(w, memoTxResult(err))
		return
	}
	var rec memoTidyRecord
	_ = json.Unmarshal([]byte(out.Run.Ops), &rec)
	d := memoTidyUndoWire{Run: memoTidyRunToDTO(out.Run, rec), Restored: make([]memoDTO, 0, len(out.Restored)), Removed: out.Removed}
	if d.Removed == nil {
		d.Removed = []string{}
	}
	for _, m := range out.Restored {
		d.Restored = append(d.Restored, memoToDTO(m))
	}
	writeJSON(w, http.StatusOK, d)
}

// --- The manual Tidy ----------------------------------------------------------------------------
//
// ADR 0137 section 8: the member selects messages of one auto-run category, the
// Console's assistant proposes operations, the member reads a preview and approves, and this route
// applies them as one run that can be undone. The proposal never reaches this file: the Console asks
// POST /api/chat/ask, so the Agent is not called here and the category's tidy setting is not read.
// The person's reading of the preview is the check the automatic run does not have, and it
// releases nothing: apply decides every effect from the stored settings, exactly as for an
// automatic run.

// memoTidyApplyBodyMax bounds the request: 40 ids and hashes and at most 80 operations of 16 KiB.
const memoTidyApplyBodyMax = 4 << 20

// memoTidyApplyItem is a message of the preview: the id and the content hash the member saw.
type memoTidyApplyItem struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

// memoTidyApplyBody is POST /api/memo-tidy/apply. Operations stays raw: memotidy.Parse reads it
// strictly, as it reads the Agent's reply.
type memoTidyApplyBody struct {
	Repo       string              `json:"repo"`
	Category   string              `json:"category"`
	Items      []memoTidyApplyItem `json:"items"`
	Operations json.RawMessage     `json:"operations"`
}

// memoTidyApplyWire is the 200: the run as the run list shows it, and the messages it touched.
type memoTidyApplyWire struct {
	Run   memoTidyRunDTO `json:"run"`
	Memos []memoDTO      `json:"memos"`
}

// memoTidyErrWire is the 409 and 400 of apply: the usual error envelope plus what the Console needs
// to say what happened. Stale names the messages that changed since the preview, TidyCode the
// validator's refusal.
type memoTidyErrWire struct {
	Error struct {
		Code     string   `json:"code"`
		Message  string   `json:"message"`
		Stale    []string `json:"stale,omitempty"`
		TidyCode string   `json:"tidyCode,omitempty"`
	} `json:"error"`
}

func writeMemoTidyErr(w http.ResponseWriter, status int, code, message string, stale []string, tidyCode string) {
	var body memoTidyErrWire
	body.Error.Code, body.Error.Message, body.Error.Stale, body.Error.TidyCode = code, message, stale, tidyCode
	writeJSON(w, status, body)
}

// applyTidy (POST /api/memo-tidy/apply) applies the operations of an approved preview as one manual
// run. Console only: no route of the token face or /mcp reaches it. It answers
//
//	200 {run, memos}                    applied or kept (a valid reply that changed nothing)
//	409 memo_tidy_stale  {stale}        a message differs from the preview: nothing was changed
//	400 memo_tidy_invalid {tidyCode}    the operations were refused whole: nothing was changed
//	409 memo_tidy_busy                  the member's other tidy run is in flight
//	404                                 the category is not an auto-run category of the member's, or a message is not in it
func (a memoAPI) applyTidy(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	ctx := r.Context()
	var in memoTidyApplyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, memoTidyApplyBodyMax)).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if len(in.Items) == 0 || len(in.Items) > memotidy.MaxInputItems {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "items must hold between 1 and 40 messages"})
		return
	}
	ids := make([]string, 0, len(in.Items))
	for _, it := range in.Items {
		if it.ID == "" || it.ContentHash == "" || slices.Contains(ids, it.ID) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "every item needs its own id and contentHash"})
			return
		}
		ids = append(ids, it.ID)
	}
	// An unreadable reply is refused before any run exists, as the Agent's reply is.
	ops, ref := memotidy.Parse(append(append([]byte(`{"operations":`), in.Operations...), '}'))
	if ref != nil {
		writeMemoTidyErr(w, http.StatusBadRequest, errCodeMemoTidyInvalid, "the operations were refused: "+ref.Code, nil, ref.Code)
		return
	}

	cats, err := a.store.ListCategories(ctx, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	queues, err := a.store.ListMemoQueues(ctx, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var cat store.MemoCategory
	for _, c := range cats {
		if c.Repo == in.Repo && c.Name == in.Category {
			cat = c
		}
	}
	var queue store.MemoQueue
	for _, q := range queues {
		if q.CategoryID == cat.ID && cat.ID != "" {
			queue = q
		}
	}
	if queue.CategoryID == "" {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category is not an auto-run category"})
		return
	}

	// What the member saw is what is applied: a message that is no longer so, or is not in this
	// category, refuses the whole call before a run exists.
	input := make([]memoTidyItem, 0, len(in.Items))
	var stale []string
	var missing bool
	read := func() error {
		stale, missing, input = nil, false, input[:0]
		return a.store.MemoTidyRead(ctx, mv.MembershipID, func(rd store.MemoTidyReader) error {
			for _, it := range in.Items {
				m, ok, err := rd.GetMemo(it.ID)
				if err != nil {
					return err
				}
				if !ok || m.Repo != cat.Repo || m.Category != cat.Name {
					missing = true
					return nil
				}
				if memoContentHash(m) != it.ContentHash || (m.State != store.MemoStatePending && m.State != store.MemoStateHeld) ||
					m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
					stale = append(stale, m.ID)
				}
				input = append(input, memoTidyItem{ID: m.ID, Hash: it.ContentHash, State: m.State})
			}
			return nil
		})
	}
	if err := read(); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if missing {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "memo not found"})
		return
	}
	if len(stale) > 0 {
		writeMemoTidyErr(w, http.StatusConflict, errCodeMemoTidyStale, "a message changed since it was shown", stale, "")
		return
	}

	offered := memoTidyDestinations(cat, queue.TidyDestinations, cats, queues)
	destIDs := make([]string, 0, len(offered))
	for _, d := range offered {
		destIDs = append(destIDs, d.ID)
	}
	out, err := memoTidyApply(ctx, a.store, memoTidyApplyIn{MembershipID: mv.MembershipID, QueueID: cat.ID, Trigger: store.TidyTriggerManual,
		Input: slices.Clone(input), Destinations: destIDs, Ops: ops})
	if err != nil {
		// The member has one run at a time, and an automatic run may be in flight.
		busy := false
		_ = a.store.MemoRead(ctx, mv.MembershipID, func(rd store.MemoWaitOps) error {
			_, busy, _ = rd.RunningTidyRun()
			return nil
		})
		if busy {
			writeMemoTidyErr(w, http.StatusConflict, errCodeMemoTidyBusy, "a tidy run is in progress", nil, "")
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	switch {
	case out.Run.State == store.TidyRunDropped && out.Run.Reason == memoTidyReasonStaleInput:
		// A message changed between the read above and apply's lock: name the ones that differ now.
		if err := read(); err == nil && len(stale) == 0 {
			stale = ids
		}
		writeMemoTidyErr(w, http.StatusConflict, errCodeMemoTidyStale, "a message changed since it was shown", stale, "")
		return
	case out.Run.State == store.TidyRunDropped && out.Run.Reason == memoTidyReasonNoQueue:
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category is not an auto-run category"})
		return
	case out.Run.State == store.TidyRunDropped:
		writeMemoTidyErr(w, http.StatusBadRequest, errCodeMemoTidyInvalid, "the operations were refused: "+out.Run.Reason, nil, out.Run.Reason)
		return
	}

	var rec memoTidyRecord
	if out.Run.Ops != "" {
		_ = json.Unmarshal([]byte(out.Run.Ops), &rec)
	}
	wire := memoTidyApplyWire{Memos: []memoDTO{}}
	touched := slices.Concat(out.Sources, out.Results, out.Moved)
	err = a.store.MemoTidyRead(ctx, mv.MembershipID, func(rd store.MemoTidyReader) error {
		// The run is already committed: a failed undo check must not turn the answer into a 500 the
		// member would retry into a stale 409. Fall back to the unchecked run (undoable:false); the
		// run list recomputes the verdict.
		var cerr error
		if wire.Run, cerr = memoTidyRunToCheckedDTO(rd, out.Run, rec); cerr != nil {
			wire.Run = memoTidyRunToDTO(out.Run, rec)
		}
		for _, id := range touched {
			m, ok, err := rd.GetMemo(id)
			if err != nil {
				return err
			}
			if ok {
				wire.Memos = append(wire.Memos, memoToDTO(m))
			}
		}
		return nil
	})
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, wire)
}
