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
)

// MemoDelivery is one fire of a queue. QueueID is the queue's category id.
type MemoDelivery struct {
	ID, MembershipID, QueueID string
	State, Outcome, Session   string
	MessageIDs                []string
	// IdleTicks counts the consecutive scheduler ticks that saw the session idle.
	IdleTicks                                        int
	CreatedAt, SentAt, OpenedAt, ClosedAt, UpdatedAt string
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
	// ListLiveMemoDeliveries returns every delivery that is not closed, of every member,
	// oldest first: the scheduler's work.
	ListLiveMemoDeliveries(ctx context.Context) ([]MemoDelivery, error)
	// MarkMemoDeliveryIdle records how many consecutive ticks saw an open delivery's session
	// idle. False means the delivery is no longer open.
	MarkMemoDeliveryIdle(ctx context.Context, id string, ticks int) (bool, error)
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
}

const memoDeliveryCols = `SELECT id, membership_id, queue_id, state, outcome, session, message_ids, idle_ticks,
	created_at, sent_at, opened_at, closed_at, updated_at FROM memo_delivery`

func scanMemoDelivery(row scanner) (MemoDelivery, error) {
	var d MemoDelivery
	var ids string
	err := row.Scan(&d.ID, &d.MembershipID, &d.QueueID, &d.State, &d.Outcome, &d.Session, &ids, &d.IdleTicks,
		&d.CreatedAt, &d.SentAt, &d.OpenedAt, &d.ClosedAt, &d.UpdatedAt)
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
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE membership_id=? AND queue_id=?
		ORDER BY created_at DESC, id DESC LIMIT ?`, membershipID, queueID, limit))
}

func (s *SQL) ListLiveMemoDeliveries(ctx context.Context) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state IN (?,?,?)
		ORDER BY created_at, id`, DeliveryClaimed, DeliverySending, DeliveryOpen))
}

func (s *SQL) MarkMemoDeliveryIdle(ctx context.Context, id string, ticks int) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET idle_ticks=?, updated_at=? WHERE id=? AND state=?`,
		ticks, DeliveryTS(time.Now()), id, DeliveryOpen))
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
		message_ids, idle_ticks, created_at, sent_at, opened_at, closed_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, t.mid, d.QueueID, d.State, d.Outcome, d.Session, string(ids), d.IdleTicks,
		d.CreatedAt, d.SentAt, d.OpenedAt, d.ClosedAt, d.UpdatedAt)
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

func (t memoTx) ClaimMemo(seen Memo, delivery string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, delivery_id=?
		WHERE id=? AND membership_id=? AND state=? AND sent_at='' AND send_token='' AND delivery_id='' AND release_hash=?
		  AND repo=? AND category=? AND kind=? AND body=? AND ref_path=? AND attachments=?`,
		MemoStateClaimed, delivery, seen.ID, t.mid, MemoStatePending, seen.ReleaseHash,
		seen.Repo, seen.Category, seen.Kind, seen.Body, seen.RefPath, seen.Attachments))
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
		  release_ref_path=ref_path, release_attachments=attachments
		WHERE membership_id=? AND delivery_id=? AND state=?`,
		MemoStatePending, t.mid, delivery, MemoStateClaimed)
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
