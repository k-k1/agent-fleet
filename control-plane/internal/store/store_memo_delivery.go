package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Message queues, step 1c (ADR 0137 section 6). A delivery is one fire of a queue. The rules
// that move it live in the API layer (control-plane/memo_delivery.go). This file gives them two
// guarantees: a slot is reserved inside MemoTx, behind the membership row's lock, so two
// reservations of one member never read the same free slot; and every move of a delivery's
// state is one conditional write on the delivery's own row, so of two writers that both saw a
// state only one moves it on.

// Delivery states (memo_delivery.state).
const (
	DeliveryClaimed = "claimed"
	DeliverySending = "sending"
	DeliveryOpen    = "open"
	DeliveryClosed  = "closed"
)

// Delivery outcomes (memo_delivery.outcome), set when a delivery closes.
const (
	DeliveryClean   = "clean"
	DeliveryFailed  = "failed"
	DeliveryAborted = "aborted"
	DeliveryUnknown = "unknown"
	DeliveryNotSent = "not_sent"
	// DeliveryEnded is a delivery a person ended from the run history: the slot is freed and the
	// session is retired, but the session is not archived, so the run is not an abort.
	DeliveryEnded = "ended"
	// DeliveryWake is a closed row with no messages and no session: a wake made only to settle
	// the deliveries of a stopped workspace (ADR 0137 section 6), kept so that it counts as a
	// fire of the day.
	DeliveryWake = "wake"
	// DeliveryDeferred is a clean close that applied the session's hand-back (defer_queue_item):
	// its messages went back to pending with a not_before, or to held on the third time. The
	// circuit breaker skips it (RecentTurnDeliveries).
	DeliveryDeferred = "deferred"
)

// MemoDelivery is one fire of a queue. QueueID is the queue's category id.
type MemoDelivery struct {
	ID, MembershipID, QueueID string
	State, Outcome, Session   string
	MessageIDs                []string
	// IdleTicks counts the consecutive scheduler ticks that saw nothing hold the delivery.
	IdleTicks                                        int
	CreatedAt, SentAt, OpenedAt, ClosedAt, UpdatedAt string

	// The last report of the Agent that was actually read (ADR 0137 section 6), kept with the
	// delivery so that the answer does not change while the workspace is stopped.
	// HeldBy is one of the Held* values, empty when nothing held the delivery at the read that
	// began at HeldSeenAt, and HeldSession the session of the tree that held it. ProgressSeenAt
	// is when a session of the tree last showed progress. Reported is set once a read of an
	// Agent that sends the queue report has been kept: only then is this a report and not a
	// default.
	HeldBy, HeldSession, HeldSeenAt, ProgressSeenAt string
	// HeldDetail is the JSON array of the watches and timers behind a HeldBy of wakers (id and
	// source only, never a payload), "" when none was listed. An old row and an Agent that lists
	// none both read "".
	HeldDetail string
	// ReadGen is the last read reserved for the delivery, HeldGen the one behind the kept report.
	ReadGen, HeldGen int64
	// ActMark is the Agent's actMark of the kept report (0: it carried none). A retire sends it
	// as ifNoActSince.
	ActMark int64
	// ActIncarnation is the identity of the Agent's counter run that handed ActMark out ("": the
	// Agent sent none). The retire echoes it, and the Agent judges a person's act against the list by it.
	ActIncarnation string
	// RetireActMark is the mark one retire decision and its unconfirmed retries all send, frozen
	// when the decision is made (the clean close, or the first attempt of an overdue retire) and
	// cleared with retired_at when progress is seen. 0: none yet, or the Agent sent no mark.
	RetireActMark int64
	// RetireActIncarnation is ActIncarnation frozen with RetireActMark.
	RetireActIncarnation string
	// RetireGen is the generation (HeldGen) of the report the overdue retire's condition was
	// frozen from: 0 means not frozen, and a frozen mark of 0 is a frozen "no mark".
	RetireGen int64
	Reported  bool
	// OverdueAt is when the delivery passed its queue's max_task_hours. RetirePending is set by
	// a clean close until the Agent confirms the retire, RetiredAt is when it last did.
	OverdueAt, RetiredAt string
	RetirePending        bool
	// RetireResult is how a pending retire ended: confirmed, released (a person started the
	// session after the retire began), done, gone or fault.
	RetireResult string
	// LimitKinds is ",kind,kind," of the sessions of the tree that waited for a usage limit at the
	// last kept report, independent of the strongest reason in HeldBy.
	LimitKinds string
	// Drain is set for a delivery claimed while its queue was draining.
	Drain bool
	// Auto is set for a delivery the dispatcher fired by itself, not one a person pressed.
	Auto bool
	// TurnEndedAt is "" until the first turn of the delivery's session was seen to end. It and
	// the two fields beside it are written once.
	TurnOutcome, TurnEndedAt string
	TurnMs                   int64
	// The PR snapshot of the delivery's session (ADR 0137, addendum 2026-11-19, part A, step 2):
	// PRSnapshot is the JSON array of its PRs and their states, PRState what they sum up to (the
	// PRState* values, "" until a read answered). PRReadAt is the last read that answered,
	// PRTriedAt the last read begun, PRChangedAt when the snapshot last changed.
	PRSnapshot, PRState, PRReadAt, PRTriedAt, PRChangedAt string
	// The last answer of the delivery's root session, read after the close (store_memo_answer.go):
	// AnswerHead is its first line, AnswerClass how it reads (the Answer* values, "" until a read
	// decided it), AnswerTriedAt the last read begun.
	AnswerHead, AnswerClass, AnswerTriedAt string
	// CloseTurnEndAt is the end of the root session's last turn (the Agent's clock) as a kept read
	// of the Agent saw it, "" when it showed none, and CloseTurnGen that read's number: both written
	// with every kept read while the delivery is open. The answer read is pinned to it (issue #4891)
	// only while CloseTurnGen is HeldGen, the read the close was based on: a read kept by an older
	// Control Plane moves HeldGen alone, and a close made without a report forgets the pin.
	CloseTurnEndAt string
	CloseTurnGen   int64
	// The hand-back the delivery's session asked for (store_memo_defer.go): DeferReason,
	// DeferNotBefore and DeferAt (the Agent's clock) are "" for none. DeferResult is what the
	// close made of it, ReturnBlocked the JSON array of the messages another message's reference
	// kept where they were, ReturnedAt when a person returned the run's messages to the queue.
	DeferReason, DeferNotBefore, DeferAt, DeferResult, ReturnBlocked, ReturnedAt string
}

// What holds an open delivery (memo_delivery.held_by).
const (
	HeldMachine = "machine"
	HeldPerson  = "person"
	HeldLimit   = "limit"
	HeldWakers  = "wakers"
	HeldPeer    = "peer"
	HeldUnknown = "unknown"
)

// DeliveryObservation is one read of the Agent for an open delivery. Gen is the number the
// database handed out for the read before it began (ReserveDeliveryReads): of two reads of one
// delivery the higher number is the later, whatever the clocks of the Control Planes say. At is
// the read's start by the local clock, kept for display only.
type DeliveryObservation struct {
	Gen                 int64
	At                  string
	HeldBy, HeldSession string
	// HeldDetail is MemoDelivery.HeldDetail for this read.
	HeldDetail string
	// Progress says a session of the tree showed progress, at ProgressAt (the tick's time).
	Progress bool
	// LimitKinds is the kinds of the sessions of the tree waiting for a usage limit, ",a,b," or "".
	LimitKinds               string
	ProgressAt               string
	TurnOutcome, TurnEndedAt string
	TurnMs                   int64
	// ActMark is the actMark the read's envelope carried, 0 when it carried none, and
	// ActIncarnation the identity that came with it ("" when none).
	ActMark        int64
	ActIncarnation string
	// RootTurnEndAt is the root session's lastTurnEndAt in this read, "" when it had none.
	RootTurnEndAt string
}

// DeliveryTS formats a delivery timestamp. Fixed width, so that the text order of two stamps
// is their time order: RFC3339Nano drops trailing zeros and "05Z" sorts after "05.1Z".
func DeliveryTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// MemoDeliveryStore is the delivery half of the memo store, for the reads and writes that need
// no membership lock: no slot is taken by them and no memo is written.
type MemoDeliveryStore interface {
	GetMemoDelivery(ctx context.Context, id string) (MemoDelivery, bool, error)
	// ListMemoDeliveries returns a queue's deliveries, newest first, at most limit.
	ListMemoDeliveries(ctx context.Context, membershipID, queueID string, limit int) ([]MemoDelivery, error)
	// ListMemoDeliveriesWithOpen is ListMemoDeliveries plus every delivery that is not closed,
	// however old, read in one statement so both parts come from one snapshot. The member-wide limit
	// on open deliveries bounds the extra.
	ListMemoDeliveriesWithOpen(ctx context.Context, membershipID, queueID string, limit int) ([]MemoDelivery, error)
	// ListMemoDeliveriesClosedSince is ListMemoDeliveriesWithOpen plus every delivery closed at or
	// after since (a DeliveryTS stamp), at most MemoClosedSinceMax of them (the newest closed
	// first), so a viewer that saw a run open still gets its end after it left the newest limit.
	ListMemoDeliveriesClosedSince(ctx context.Context, membershipID, queueID string, limit int, since string) ([]MemoDelivery, error)
	// ListClosedWithoutTurn returns the deliveries closed at or after since with a session and no
	// first turn recorded, the least recently tried first (never tried before tried), at most limit.
	// MarkTurnTried stamps the ones a pass has read, so the next pass goes on with the others.
	ListClosedWithoutTurn(ctx context.Context, since string, limit int) ([]MemoDelivery, error)
	MarkTurnTried(ctx context.Context, ids []string, at string) error
	// RecordDeliveryFirstTurn copies a first turn onto a delivery that has none (open or closed).
	RecordDeliveryFirstTurn(ctx context.Context, id, outcome string, ms int64, endedAt string) (bool, error)
	// ListLiveMemoDeliveries returns every delivery that is not closed, of every member,
	// oldest first: the scheduler's work.
	ListLiveMemoDeliveries(ctx context.Context) ([]MemoDelivery, error)
	// MarkMemoDeliveryIdle records how many consecutive ticks saw an open delivery's session
	// idle. False means the delivery is no longer open.
	MarkMemoDeliveryIdle(ctx context.Context, id string, ticks int) (bool, error)
	// MarkMemoDeliveryOverdue records that the member was told the delivery is overdue. False
	// means it was already recorded, or the delivery is no longer open.
	MarkMemoDeliveryOverdue(ctx context.Context, id, at string) (bool, error)
	// MarkMemoDeliveryRetired records an Agent's confirmed retire of an open delivery's tree.
	// The write only lands while the retire's frozen generation is still gen (progress clears it).
	MarkMemoDeliveryRetired(ctx context.Context, id, at string, gen int64) (bool, error)
	// FreezeDeliveryRetireMark fixes the condition an open delivery's overdue retire sends, in one
	// conditional write: it takes mark of the report with generation gen (the snapshot the retire
	// was decided on) only while that report is still the kept one and nothing is frozen. When a
	// condition is already frozen it is returned, a frozen mark of 0 included. ok is false when a
	// newer report was kept before the freeze: nothing is sent and the next tick decides again.
	// The mark's incarnation is frozen with it and returned the same way.
	FreezeDeliveryRetireMark(ctx context.Context, id string, gen, mark int64, incarnation string) (frozenMark int64, frozenIncarnation string, frozenGen int64, ok bool, err error)
	// ListRetirePendingDeliveries returns the closed deliveries whose session still has to be
	// retired, oldest first: the ones marked pending, and every clean close of a reported
	// delivery without a result (an older binary's close sets no mark).
	ListRetirePendingDeliveries(ctx context.Context) ([]MemoDelivery, error)
	// ListDeferredToSettle: see store_memo_defer.go.
	ListDeferredToSettle(ctx context.Context) ([]MemoDelivery, error)
	// FinishDeliveryRetire clears the pending retire of a closed delivery and keeps result
	// (confirmed, released, done, gone, fault). retired_at is set only for a confirmed one.
	FinishDeliveryRetire(ctx context.Context, id, at, result string) (bool, error)
}

// MemoDeliveryTxOps are the delivery reads and writes a MemoTx may make, scoped to its
// membership.
type MemoDeliveryTxOps interface {
	// CountLiveDeliveries counts the deliveries that are not closed: of the queue, and of the
	// whole membership.
	CountLiveDeliveries(queueID string) (queue, member int, err error)
	InsertDelivery(d MemoDelivery) error
	GetDelivery(id string) (MemoDelivery, bool, error)
	// CountUnsentDeliveries counts the membership's deliveries in claimed or sending: the
	// ones that have no session yet.
	CountUnsentDeliveries() (int, error)
	// CountDeliveriesCreatedSince counts the queue's deliveries created at or after since,
	// whatever their state: the fires of the day, wakes included.
	CountDeliveriesCreatedSince(queueID, since string) (int, error)
	// CountWakesSince counts the queue's wakes made only to settle, created at or after since.
	CountWakesSince(queueID, since string) (int, error)
	// CountMemberWakesSince counts the wakes of every queue of the member since, which share
	// one workspace.
	CountMemberWakesSince(since string) (int, error)
	// MarkDeliveryDrain marks a delivery as claimed while its queue was draining.
	MarkDeliveryDrain(id string) error
	// CountDeliveriesStartedSince counts the membership's deliveries other than except that
	// are sending, or were sent or opened at or after since: the ones whose session a session
	// count taken at since may not show.
	CountDeliveriesStartedSince(since, except string) (int, error)
	// ClaimMemo moves one pending message to claimed by delivery, only if the row still holds
	// exactly the content and the release of seen, unsent, in no flush and claimed by none. It
	// is one conditional write on the row itself. False means nothing was written.
	ClaimMemo(seen Memo, delivery string) (bool, error)
	// MoveDelivery moves a delivery from one state to another, only if it is still in from.
	// It stamps the time the new state is entered at, and session (open) or outcome (closed).
	MoveDelivery(id, from, to, session, outcome, at string) (bool, error)
	// ReturnDeliveryMessages puts the messages a delivery still holds claimed back to pending
	// under the release they were claimed with.
	ReturnDeliveryMessages(delivery string) error
	// DeliverDeliveryMessages marks them delivered and sent at at.
	DeliverDeliveryMessages(delivery, at string) error
	// UnconfirmDeliveryMessages marks them unconfirmed: never fired again by themselves.
	UnconfirmDeliveryMessages(delivery string) error
	// ReserveDeliveryReads hands out, for each open delivery of the membership, the number of
	// the read about to begin. Call it before the Agent is asked.
	ReserveDeliveryReads() (map[string]int64, error)
	// ObserveDelivery keeps a read of an Agent that sends the queue report with an open delivery
	// and answers the consecutive reads that found nothing holding it (0, 1 or 2). A count counts as
	// a previous clear read only when the stored row was itself clear (held_by empty before this
	// update): MarkMemoDeliveryIdle, the rule for an Agent that does not report, writes idle_ticks
	// whatever the kept report says, and that is no clear read of the report. The count is
	// worked out from the stored row, never from a copy read earlier. A read whose number is
	// older than the kept one is refused (kept false): it still resets the count when it found
	// a hold, so a stale read can never help a close. Progress clears retired_at, and a first
	// turn is copied only while none is recorded.
	ObserveDelivery(id string, o DeliveryObservation) (idleTicks int, kept bool, err error)
	// SetDeliveryIdleTicks sets the count of an open delivery that no report was read for.
	SetDeliveryIdleTicks(id string, ticks int) error
	// SetDeliveryRetirePending marks a closed delivery as awaiting the Agent's confirmed retire.
	// Called in the transaction of the clean close, so the close and the mark are one write.
	SetDeliveryRetirePending(id string) error
	// ForgetDeliveryCloseTurn drops the pin of the answer read (CloseTurnEndAt) of a delivery
	// closed without a report: the last kept read is not what the close was based on.
	ForgetDeliveryCloseTurn(id string) error
	// SetDeliveryRetireForced makes the pending retire of a closed delivery unconditional: it
	// drops the act mark the retire would send, so a person's own later activity in the session
	// does not turn the retire into "released". For a person's explicit end only.
	SetDeliveryRetireForced(id string) error
}

const memoDeliveryCols = `SELECT id, membership_id, queue_id, state, outcome, session, message_ids, idle_ticks,
	created_at, sent_at, opened_at, closed_at, updated_at,
	held_by, held_session, held_seen_at, progress_seen_at, read_gen, held_gen, reported, overdue_at, retire_pending, retired_at, retire_result,
	turn_outcome, turn_ms, turn_ended_at, auto, limit_kinds, drain, act_mark, retire_act_mark, retire_gen, act_incarnation, retire_act_incarnation, held_detail,
	pr_snapshot, pr_state, pr_read_at, pr_tried_at, pr_changed_at, answer_head, answer_class, answer_tried_at, close_turn_end_at, close_turn_gen,
	defer_reason, defer_not_before, defer_at, defer_result, return_blocked, returned_at FROM memo_delivery`

func scanMemoDelivery(row scanner) (MemoDelivery, error) {
	var d MemoDelivery
	var ids string
	var reported, retire, auto, drain int
	err := row.Scan(&d.ID, &d.MembershipID, &d.QueueID, &d.State, &d.Outcome, &d.Session, &ids, &d.IdleTicks,
		&d.CreatedAt, &d.SentAt, &d.OpenedAt, &d.ClosedAt, &d.UpdatedAt,
		&d.HeldBy, &d.HeldSession, &d.HeldSeenAt, &d.ProgressSeenAt, &d.ReadGen, &d.HeldGen, &reported, &d.OverdueAt, &retire, &d.RetiredAt, &d.RetireResult,
		&d.TurnOutcome, &d.TurnMs, &d.TurnEndedAt, &auto, &d.LimitKinds, &drain, &d.ActMark, &d.RetireActMark, &d.RetireGen, &d.ActIncarnation, &d.RetireActIncarnation, &d.HeldDetail,
		&d.PRSnapshot, &d.PRState, &d.PRReadAt, &d.PRTriedAt, &d.PRChangedAt, &d.AnswerHead, &d.AnswerClass, &d.AnswerTriedAt, &d.CloseTurnEndAt, &d.CloseTurnGen,
		&d.DeferReason, &d.DeferNotBefore, &d.DeferAt, &d.DeferResult, &d.ReturnBlocked, &d.ReturnedAt)
	d.Reported, d.RetirePending, d.Auto, d.Drain = reported != 0, retire != 0, auto != 0, drain != 0
	d.MessageIDs = []string{}
	if err == nil && ids != "" {
		_ = json.Unmarshal([]byte(ids), &d.MessageIDs)
	}
	return d, err
}

func scanMemoDeliveries(rows *sql.Rows, err error) ([]MemoDelivery, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoDelivery{}
	for rows.Next() {
		d, err := scanMemoDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQL) GetMemoDelivery(ctx context.Context, id string) (MemoDelivery, bool, error) {
	d, err := scanMemoDelivery(s.db.QueryRowContext(ctx, memoDeliveryCols+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoDelivery{}, false, nil
	}
	return d, err == nil, err
}

func (s *SQL) ListMemoDeliveries(ctx context.Context, membershipID, queueID string, limit int) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND outcome<>?
		ORDER BY created_at DESC, id DESC LIMIT ?`, membershipID, queueID, DeliveryWake, limit))
}

func (s *SQL) ListMemoDeliveriesWithOpen(ctx context.Context, membershipID, queueID string, limit int) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND outcome<>?
		AND (state<>? OR id IN (SELECT id FROM memo_delivery WHERE membership_id=? AND queue_id=? AND outcome<>?
			ORDER BY created_at DESC, id DESC LIMIT ?))
		ORDER BY created_at DESC, id DESC`,
		membershipID, queueID, DeliveryWake, DeliveryClosed, membershipID, queueID, DeliveryWake, limit))
}

// MemoClosedSinceMax bounds the closed-since part of a list, so a stale cursor cannot ask for a queue's whole history.
const MemoClosedSinceMax = 500

func (s *SQL) ListMemoDeliveriesClosedSince(ctx context.Context, membershipID, queueID string, limit int, since string) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND outcome<>?
		AND (state<>?
			OR id IN (SELECT id FROM memo_delivery WHERE membership_id=? AND queue_id=? AND outcome<>?
				ORDER BY created_at DESC, id DESC LIMIT ?)
			OR id IN (SELECT id FROM memo_delivery WHERE membership_id=? AND queue_id=? AND outcome<>? AND state=? AND closed_at>=?
				ORDER BY closed_at DESC, id DESC LIMIT ?))
		ORDER BY created_at DESC, id DESC`,
		membershipID, queueID, DeliveryWake, DeliveryClosed,
		membershipID, queueID, DeliveryWake, limit,
		membershipID, queueID, DeliveryWake, DeliveryClosed, since, MemoClosedSinceMax))
}

func (s *SQL) ListLiveMemoDeliveries(ctx context.Context) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state IN (?,?,?)
		ORDER BY created_at, id`, DeliveryClaimed, DeliverySending, DeliveryOpen))
}

func (t memoTx) queryDeliveries(q string, args ...any) ([]MemoDelivery, error) {
	rows, err := t.q.QueryContext(t.ctx, q, args...)
	return scanMemoDeliveries(rows, err)
}

func (t memoTx) RecentQueueDeliveries(queueID string, limit int) ([]MemoDelivery, error) {
	return t.queryDeliveries(memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND outcome<>?
		ORDER BY created_at DESC, id DESC LIMIT ?`, t.mid, queueID, DeliveryWake, limit)
}

func (t memoTx) LiveQueueDeliveries(queueID string) ([]MemoDelivery, error) {
	return t.queryDeliveries(memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND state<>? ORDER BY created_at, id`,
		t.mid, queueID, DeliveryClosed)
}

func (t memoTx) RecentTurnDeliveries(queueID, after string, limit int) ([]MemoDelivery, error) {
	// A deferred delivery is skipped: it neither counts as bad nor ends a run of bad ones, so a
	// hand-back (forged or not) cannot reset the breaker.
	return t.queryDeliveries(memoDeliveryCols+` WHERE membership_id=? AND queue_id=? AND turn_ended_at<>'' AND created_at>? AND outcome<>?
		ORDER BY created_at DESC, id DESC LIMIT ?`, t.mid, queueID, after, DeliveryDeferred, limit)
}

func (t memoTx) CountMemberWakesSince(since string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND created_at>=? AND outcome=?`,
		t.mid, since, DeliveryWake).Scan(&n)
	return n, err
}

func (t memoTx) MarkDeliveryDrain(id string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET drain=1 WHERE id=? AND membership_id=?`, id, t.mid)
	return err
}

func (s *SQL) ListClosedWithoutTurn(ctx context.Context, since string, limit int) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state=? AND session<>'' AND turn_ended_at='' AND closed_at>=?
		ORDER BY turn_tried_at, closed_at, id LIMIT ?`, DeliveryClosed, since, limit))
}

func (s *SQL) MarkTurnTried(ctx context.Context, ids []string, at string) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE memo_delivery SET turn_tried_at=? WHERE id=?`, at, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQL) RecordDeliveryFirstTurn(ctx context.Context, id, outcome string, ms int64, endedAt string) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET turn_outcome=?, turn_ms=?, turn_ended_at=? WHERE id=? AND turn_ended_at=''`,
		outcome, ms, endedAt, id))
}

func (t memoTx) CountWakesSince(queueID, since string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND queue_id=? AND created_at>=? AND outcome=?`,
		t.mid, queueID, since, DeliveryWake).Scan(&n)
	return n, err
}

func (t memoTx) CountDeliveriesCreatedSince(queueID, since string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND queue_id=? AND created_at>=?`,
		t.mid, queueID, since).Scan(&n)
	return n, err
}

func (t memoTx) NthOldestDeliverySince(queueID, since string, skip int) (string, bool, error) {
	var at string
	err := t.q.QueryRowContext(t.ctx, `SELECT created_at FROM memo_delivery WHERE membership_id=? AND queue_id=? AND created_at>=?
		ORDER BY created_at, id LIMIT 1 OFFSET ?`, t.mid, queueID, since, skip).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return at, err == nil, err
}

func (s *SQL) MarkMemoDeliveryIdle(ctx context.Context, id string, ticks int) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET idle_ticks=?, updated_at=? WHERE id=? AND state=?`,
		ticks, DeliveryTS(time.Now()), id, DeliveryOpen))
}

func (t memoTx) ReserveDeliveryReads() (map[string]int64, error) {
	if _, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET read_gen=read_gen+1 WHERE membership_id=? AND state=?`,
		t.mid, DeliveryOpen); err != nil {
		return nil, err
	}
	rows, err := t.q.QueryContext(t.ctx, `SELECT id, read_gen FROM memo_delivery WHERE membership_id=? AND state=?`, t.mid, DeliveryOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var gen int64
		if err := rows.Scan(&id, &gen); err != nil {
			return nil, err
		}
		out[id] = gen
	}
	return out, rows.Err()
}

func (t memoTx) ObserveDelivery(id string, o DeliveryObservation) (int, bool, error) {
	held, progress := 0, 0
	if o.HeldBy != "" {
		held = 1
	}
	if o.Progress {
		progress = 1
	}
	kept, err := affected(t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET held_by=?, held_session=?, held_detail=?, held_seen_at=?, held_gen=?, limit_kinds=?, act_mark=?, act_incarnation=?,
		  reported=1, updated_at=?, close_turn_end_at=?, close_turn_gen=?,
		  idle_ticks=CASE WHEN ?=1 THEN 0 WHEN reported=1 AND held_by='' AND idle_ticks>=1 THEN 2 ELSE 1 END,
		  progress_seen_at=CASE WHEN ?=1 THEN ? ELSE progress_seen_at END,
		  retired_at=CASE WHEN ?=1 THEN '' ELSE retired_at END,
		  retire_act_mark=CASE WHEN ?=1 THEN 0 ELSE retire_act_mark END,
		  retire_act_incarnation=CASE WHEN ?=1 THEN '' ELSE retire_act_incarnation END,
		  retire_gen=CASE WHEN ?=1 THEN 0 ELSE retire_gen END,
		  turn_outcome=CASE WHEN turn_ended_at='' AND ?<>'' THEN ? ELSE turn_outcome END,
		  turn_ms=CASE WHEN turn_ended_at='' AND ?<>'' THEN ? ELSE turn_ms END,
		  turn_ended_at=CASE WHEN turn_ended_at='' THEN ? ELSE turn_ended_at END
		WHERE id=? AND membership_id=? AND state=? AND held_gen<=?`,
		o.HeldBy, o.HeldSession, o.HeldDetail, o.At, o.Gen, o.LimitKinds, o.ActMark, o.ActIncarnation, DeliveryTS(time.Now()), o.RootTurnEndAt, o.Gen,
		held, progress, o.ProgressAt, progress, progress, progress, progress,
		o.TurnEndedAt, o.TurnOutcome, o.TurnEndedAt, o.TurnMs, o.TurnEndedAt,
		id, t.mid, DeliveryOpen, o.Gen))
	if err != nil {
		return 0, false, err
	}
	if !kept && held == 1 {
		if _, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET idle_ticks=0 WHERE id=? AND membership_id=? AND state=?`,
			id, t.mid, DeliveryOpen); err != nil {
			return 0, false, err
		}
	}
	cur, ok, err := t.GetDelivery(id)
	if err != nil || !ok {
		return 0, false, err
	}
	return cur.IdleTicks, kept, nil
}

func (t memoTx) SetDeliveryIdleTicks(id string, ticks int) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET idle_ticks=? WHERE id=? AND membership_id=? AND state=?`,
		ticks, id, t.mid, DeliveryOpen)
	return err
}

func (s *SQL) MarkMemoDeliveryOverdue(ctx context.Context, id, at string) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET overdue_at=?, updated_at=? WHERE id=? AND state=? AND overdue_at=''`,
		at, at, id, DeliveryOpen))
}

func (s *SQL) FreezeDeliveryRetireMark(ctx context.Context, id string, gen, mark int64, incarnation string) (int64, string, int64, bool, error) {
	won, err := affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET retire_act_mark=?, retire_act_incarnation=?, retire_gen=?
		WHERE id=? AND state=? AND held_gen=? AND retire_gen=0`, mark, incarnation, gen, id, DeliveryOpen, gen))
	if err != nil {
		return 0, "", 0, false, err
	}
	if won {
		return mark, incarnation, gen, true, nil
	}
	var m, g int64
	var inc string
	if err := s.db.QueryRowContext(ctx, `SELECT retire_act_mark, retire_act_incarnation, retire_gen FROM memo_delivery WHERE id=? AND state=?`,
		id, DeliveryOpen).Scan(&m, &inc, &g); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", 0, false, nil
		}
		return 0, "", 0, false, err
	}
	return m, inc, g, g != 0, nil
}

func (s *SQL) MarkMemoDeliveryRetired(ctx context.Context, id, at string, gen int64) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET retired_at=?, updated_at=? WHERE id=? AND state=? AND retire_gen=?`,
		at, at, id, DeliveryOpen, gen))
}

func (s *SQL) ListRetirePendingDeliveries(ctx context.Context) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state=? AND retire_result='' AND (retire_pending=1 OR (outcome=? AND reported=1))
		ORDER BY closed_at, id`, DeliveryClosed, DeliveryClean))
}

func (s *SQL) FinishDeliveryRetire(ctx context.Context, id, at, result string) (bool, error) {
	retired := ""
	if result == "confirmed" {
		retired = at
	}
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET retire_pending=0, retired_at=?, retire_result=?, updated_at=?
		WHERE id=? AND state=? AND retire_result=''`, retired, result, at, id, DeliveryClosed))
}

func (t memoTx) ForgetDeliveryCloseTurn(id string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET close_turn_end_at='', close_turn_gen=0 WHERE id=? AND membership_id=?`, id, t.mid)
	return err
}

func (t memoTx) SetDeliveryRetirePending(id string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET retire_pending=1, retire_act_mark=act_mark, retire_act_incarnation=act_incarnation WHERE id=? AND membership_id=? AND state=?`,
		id, t.mid, DeliveryClosed)
	return err
}

func (t memoTx) SetDeliveryRetireForced(id string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET retire_pending=1, retire_act_mark=0, retire_act_incarnation='' WHERE id=? AND membership_id=? AND state=?`,
		id, t.mid, DeliveryClosed)
	return err
}

func (t memoTx) CountLiveDeliveries(queueID string) (int, int, error) {
	var q, m int
	err := t.q.QueryRowContext(t.ctx, `SELECT
		  COALESCE(SUM(CASE WHEN queue_id=? THEN 1 ELSE 0 END), 0), COUNT(*)
		FROM memo_delivery WHERE membership_id=? AND state<>?`, queueID, t.mid, DeliveryClosed).Scan(&q, &m)
	return q, m, err
}

func (t memoTx) InsertDelivery(d MemoDelivery) error {
	ids, _ := json.Marshal(append([]string{}, d.MessageIDs...))
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_delivery(id, membership_id, queue_id, state, outcome, session,
		message_ids, idle_ticks, created_at, sent_at, opened_at, closed_at, updated_at, auto) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, t.mid, d.QueueID, d.State, d.Outcome, d.Session, string(ids), d.IdleTicks,
		d.CreatedAt, d.SentAt, d.OpenedAt, d.ClosedAt, d.UpdatedAt, b2i(d.Auto))
	return err
}

func (t memoTx) GetDelivery(id string) (MemoDelivery, bool, error) {
	d, err := scanMemoDelivery(t.q.QueryRowContext(t.ctx, memoDeliveryCols+` WHERE id=? AND membership_id=?`, id, t.mid))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoDelivery{}, false, nil
	}
	return d, err == nil, err
}

func (t memoTx) CountUnsentDeliveries() (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND state IN (?,?)`,
		t.mid, DeliveryClaimed, DeliverySending).Scan(&n)
	return n, err
}

func (t memoTx) CountDeliveriesStartedSince(since, except string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND id<>?
		AND (state=? OR sent_at>=? OR opened_at>=?)`, t.mid, except, DeliverySending, since, since).Scan(&n)
	return n, err
}

func (t memoTx) ClaimMemo(seen Memo, delivery string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, delivery_id=?
		WHERE id=? AND membership_id=? AND state=? AND sent_at='' AND send_token='' AND delivery_id='' AND release_hash=?
		  AND repo=? AND category=? AND kind=? AND body=? AND ref_path=? AND attachments=?
		  AND ref_provider=? AND ref_key=? AND ref_url=? AND after_ids=? AND start_after=? AND recipe=?`,
		MemoStateClaimed, delivery, seen.ID, t.mid, MemoStatePending, seen.ReleaseHash,
		seen.Repo, seen.Category, seen.Kind, seen.Body, seen.RefPath, seen.Attachments,
		seen.RefProvider, seen.RefKey, seen.RefURL, seen.AfterIDs, seen.StartAfter, seen.Recipe))
}

// MoveDelivery's guard is the state of the row it writes, never a read before it: the read
// would let two writers that saw the same state both pass.
func (t memoTx) MoveDelivery(id, from, to, session, outcome, at string) (bool, error) {
	set := []string{"state=?", "updated_at=?"}
	args := []any{to, at}
	switch to {
	case DeliverySending:
		set = append(set, "sent_at=?")
		args = append(args, at)
	case DeliveryOpen:
		set = append(set, "opened_at=?", "session=?", "idle_ticks=0")
		args = append(args, at, session)
	case DeliveryClosed:
		set = append(set, "closed_at=?", "outcome=?")
		args = append(args, at, outcome)
	}
	args = append(args, id, t.mid, from)
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET `+strings.Join(set, ", ")+`
		WHERE id=? AND membership_id=? AND state=?`, args...))
}

// ReturnDeliveryMessages restores the release_* copy with the state: the CHECK of migration
// 0131 refuses a pending row whose copy differs from its content. A claimed message cannot be
// changed (memoQueueMove), so its content is still what was released.
func (t memoTx) ReturnDeliveryMessages(delivery string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, delivery_id='',
		  release_repo=repo, release_category=category, release_kind=kind, release_body=body,
		  release_ref_path=ref_path, release_attachments=attachments,
		  release_ref_provider=ref_provider, release_ref_key=ref_key, release_ref_url=ref_url,
		  release_after_ids=after_ids, release_start_after=start_after, release_recipe=recipe
		WHERE membership_id=? AND delivery_id=? AND state=?`,
		MemoStatePending, t.mid, delivery, MemoStateClaimed)
	if err != nil {
		return err
	}
	// The batch that ended the drain by claiming the last pending message goes back: the queue
	// is draining again, or the returned message would wait for a threshold it already passed.
	_, err = t.q.ExecContext(t.ctx, `UPDATE memo_queue SET draining=1 WHERE membership_id=? AND category_id=
		(SELECT queue_id FROM memo_delivery WHERE id=? AND drain=1)`, t.mid, delivery)
	return err
}

func (t memoTx) DeliverDeliveryMessages(delivery, at string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, sent_at=? WHERE membership_id=? AND delivery_id=? AND state=?`,
		MemoStateDelivered, at, t.mid, delivery, MemoStateClaimed)
	return err
}

func (t memoTx) UnconfirmDeliveryMessages(delivery string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo SET state=? WHERE membership_id=? AND delivery_id=? AND state=?`,
		MemoStateUnconfirmed, t.mid, delivery, MemoStateClaimed)
	return err
}
