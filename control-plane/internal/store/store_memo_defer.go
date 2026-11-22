package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// A run that hands its message back (ADR 0137, addendum 2026-11-19, part B). The delivery's
// session asks for it with defer_queue_item, which only records the request on the delivery; a
// clean close made on a running Agent's report applies it (control-plane/memo_delivery.go), and a
// person's "return to queue" on a closed run uses the same transition. Both run behind the
// membership lock.
//
// The transition is not ReturnDeliveryMessages, which moves claimed rows only: it takes the
// messages a delivery delivered (or left unconfirmed) and makes them pending again with sent_at
// and delivery_id cleared, the release copy rewritten (the CHECKs of 0131, 0138 and 0144 refuse a
// pending row whose copy differs from its content) and the release hash kept, since a delivered
// message cannot be edited. A message whose reference another message of the queue already holds
// open stays where it is: the unique index idx_memo_open_ref would refuse the write, and a close
// must never fail on it.

// MemoDeferMax is the hand-back that sends a message to held instead of pending: the third one
// since a person last released or returned it.
const MemoDeferMax = 3

// What a close made of a delivery's hand-back request (memo_delivery.defer_result).
const (
	DeferApplied = "applied"
	// DeferDroppedTurn: a turn of the delivery's session tree started after the call.
	DeferDroppedTurn = "turn"
	// DeferDroppedPR: the session holds a PR that is open or merged (or of unknown state).
	DeferDroppedPR = "pr"
	// DeferDroppedUnread: no answer newer than the call could be read (the Agent lacks the
	// endpoint or the turn starts, or did not answer). A run that may have done the work is not
	// handed back on a doubt.
	DeferDroppedUnread = "unread"
)

// DeferDroppedReleased: the retire that a deferred close requested came back released or done (a
// person took the session over) or refused: the run is theirs, and nothing is handed back.
const DeferDroppedReleased = "released"

// DeferReturnedByPerson: a person returned the run's messages before its hand-back was decided;
// their return stands, and the hand-back is not applied.
const DeferReturnedByPerson = "returned"

// MemoHeldDeferred is memo.held_reason's reason for a message the third hand-back sent to held.
const MemoHeldDeferred = "deferred"

// MemoDeferEntry is one hand-back of a message, as memo.defer_reasons keeps it.
type MemoDeferEntry struct {
	Reason   string `json:"reason"`
	At       string `json:"at"`
	Delivery string `json:"delivery,omitempty"`
}

// ParseDeferReasons reads memo.defer_reasons, nil for none or one that does not parse.
func ParseDeferReasons(raw string) []MemoDeferEntry {
	var out []MemoDeferEntry
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// MemoReturnBlocked is a message that stayed where it was, and the open message of its queue
// that holds its reference (memo_delivery.return_blocked).
type MemoReturnBlocked struct {
	ID string `json:"id"`
	By string `json:"by"`
}

// ParseReturnBlocked reads memo_delivery.return_blocked, nil for none.
func ParseReturnBlocked(raw string) []MemoReturnBlocked {
	var out []MemoReturnBlocked
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// EncodeReturnBlocked is what memo_delivery.return_blocked holds: "" for none.
func EncodeReturnBlocked(b []MemoReturnBlocked) string {
	if len(b) == 0 {
		return ""
	}
	out, _ := json.Marshal(b)
	return string(out)
}

// MemoReturn is one use of the return transition.
type MemoReturn struct {
	// Person is the person's "return to queue": not_before and the hand-backs are cleared.
	// Otherwise it is a hand-back: the message waits for NotBefore and Entry is recorded, and the
	// MemoDeferMax-th one goes to held instead, with HeldReason.
	Person    bool
	NotBefore string
	Entry     MemoDeferEntry
	// HeldReason is memo.held_reason for a hand-back that goes to held.
	HeldReason string
	// Hash is the content hash a release binds (the API layer's memoContentHash). The person's
	// return is a release of the content as it stands now (a rename of the category since the run
	// included), so it records Hash and At; a hand-back keeps the release it ran under.
	Hash func(Memo) string
	At   string
}

// MemoReturnResult is what the transition did: the messages back in pending, the ones sent to
// held, and the ones another message's reference kept where they were.
type MemoReturnResult struct {
	Pending, Held []string
	Blocked       []MemoReturnBlocked
}

// MemoDeferTxOps are the writes of the hand-back, scoped to the MemoTx's membership.
type MemoDeferTxOps interface {
	// SetDeliveryDefer records, replaces or (reason "") withdraws the hand-back request of a
	// delivery that is open and names session. False means nothing was written.
	SetDeliveryDefer(id, session, reason, notBefore, at string) (bool, error)
	// SetDeliveryDeferResult keeps what a close made of the request, and the messages blocked.
	SetDeliveryDeferResult(id, result, blocked string) error
	// SettleDeferResult ends a deferred close whose return has not been decided yet: result
	// applied keeps the outcome deferred; any other result is a drop, and the close becomes the
	// clean one it would have been. False means it was decided already.
	SettleDeferResult(id, result, blocked string) (bool, error)
	// MarkDeliveryReturned stamps a person's return to the queue of a closed delivery.
	MarkDeliveryReturned(id, at, blocked string) error
	// OpenRefHolder is the id of another message of (repo, category) that holds the reference
	// open (held, pending, claimed), "" when none does.
	OpenRefHolder(m Memo) (string, error)
	// ReturnDelivered is the return transition for every message whose delivery_id is delivery
	// and whose state is delivered or unconfirmed.
	ReturnDelivered(delivery string, r MemoReturn) (MemoReturnResult, error)
}

func (t memoTx) SetDeliveryDefer(id, session, reason, notBefore, at string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET defer_reason=?, defer_not_before=?, defer_at=?
		WHERE id=? AND membership_id=? AND state=? AND session=? AND session<>''`,
		reason, notBefore, at, id, t.mid, DeliveryOpen, session))
}

func (t memoTx) SetDeliveryDeferResult(id, result, blocked string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET defer_result=?, return_blocked=? WHERE id=? AND membership_id=?`,
		result, blocked, id, t.mid)
	return err
}

func (t memoTx) SettleDeferResult(id, result, blocked string) (bool, error) {
	outcome := DeliveryClean
	if result == DeferApplied {
		outcome = DeliveryDeferred
	}
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET outcome=?, defer_result=?, return_blocked=?
		WHERE id=? AND membership_id=? AND state=? AND outcome=? AND defer_result=''`,
		outcome, result, blocked, id, t.mid, DeliveryClosed, DeliveryDeferred))
}

// ListDeferredToSettle returns the deferred closes whose return is not decided yet and whose
// retire has ended (or was never pending), oldest first: the settle pass decides them.
func (s *SQL) ListDeferredToSettle(ctx context.Context) ([]MemoDelivery, error) {
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state=? AND outcome=? AND defer_result=''
		  AND retire_pending=0 AND returned_at='' ORDER BY closed_at, id`, DeliveryClosed, DeliveryDeferred))
}

func (t memoTx) MarkDeliveryReturned(id, at, blocked string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_delivery SET returned_at=?, return_blocked=? WHERE id=? AND membership_id=? AND state=?`,
		at, blocked, id, t.mid, DeliveryClosed)
	return err
}

func (t memoTx) OpenRefHolder(m Memo) (string, error) {
	if m.RefKey == "" {
		return "", nil
	}
	var id string
	err := t.q.QueryRowContext(t.ctx, `SELECT id FROM memo WHERE membership_id=? AND repo=? AND category=? AND ref_provider=?
		  AND lower(ref_key)=lower(?) AND ref_key<>'' AND state IN (?,?,?) AND id<>? ORDER BY id LIMIT 1`,
		t.mid, m.Repo, m.Category, m.RefProvider, m.RefKey, MemoStateHeld, MemoStatePending, MemoStateClaimed, m.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (t memoTx) ReturnDelivered(delivery string, r MemoReturn) (MemoReturnResult, error) {
	var res MemoReturnResult
	rows, err := t.q.QueryContext(t.ctx, memoCols+` WHERE membership_id=? AND delivery_id=? AND delivery_id<>'' AND state IN (?,?)
		ORDER BY created_at, position, id`, t.mid, delivery, MemoStateDelivered, MemoStateUnconfirmed)
	if err != nil {
		return res, err
	}
	var ms []Memo
	for rows.Next() {
		m, err := scanMemo(rows)
		if err != nil {
			rows.Close()
			return res, err
		}
		ms = append(ms, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, m := range ms {
		by, err := t.OpenRefHolder(m)
		if err != nil {
			return res, err
		}
		if by != "" {
			res.Blocked = append(res.Blocked, MemoReturnBlocked{ID: m.ID, By: by})
			continue
		}
		notBefore, count, reasons := "", 0, ""
		toHeld := false
		if !r.Person {
			entries := append(ParseDeferReasons(m.DeferReasons), r.Entry)
			b, _ := json.Marshal(entries)
			notBefore, count, reasons = r.NotBefore, m.DeferCount+1, string(b)
			toHeld = count >= MemoDeferMax
		}
		guard := ` WHERE id=? AND membership_id=? AND delivery_id=? AND state IN (?,?)`
		gargs := []any{m.ID, t.mid, delivery, MemoStateDelivered, MemoStateUnconfirmed}
		if toHeld {
			// As every return to held, the release goes: a person releases it again.
			_, err = t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, sent_at='', delivery_id='',
				  release_hash='', released_at='', released_by='', held_reason=?,
				  release_repo='', release_category='', release_kind='', release_body='', release_ref_path='', release_attachments='',
				  release_ref_provider='', release_ref_key='', release_ref_url='', release_after_ids='', release_start_after='',
				  not_before='', defer_count=?, defer_reasons=?`+guard,
				append([]any{MemoStateHeld, r.HeldReason, count, reasons}, gargs...)...)
			if err != nil {
				return res, err
			}
			res.Held = append(res.Held, m.ID)
			continue
		}
		hash, releasedAt, releasedBy := m.ReleaseHash, m.ReleasedAt, m.ReleasedBy
		if r.Person && r.Hash != nil {
			hash, releasedAt, releasedBy = r.Hash(m), r.At, ""
		}
		_, err = t.q.ExecContext(t.ctx, `UPDATE memo SET state=?, sent_at='', delivery_id='', held_reason='',
			  release_hash=?, released_at=?, released_by=?,
			  release_repo=repo, release_category=category, release_kind=kind, release_body=body,
			  release_ref_path=ref_path, release_attachments=attachments,
			  release_ref_provider=ref_provider, release_ref_key=ref_key, release_ref_url=ref_url,
			  release_after_ids=after_ids, release_start_after=start_after,
			  not_before=?, defer_count=?, defer_reasons=?`+guard,
			append([]any{MemoStatePending, hash, releasedAt, releasedBy, notBefore, count, reasons}, gargs...)...)
		if err != nil {
			return res, err
		}
		res.Pending = append(res.Pending, m.ID)
	}
	if len(res.Pending) > 0 {
		// A batch that ended a drain goes back as ReturnDeliveryMessages' does: the queue drains
		// again, or the returned message would wait for a threshold it already passed.
		if _, err := t.q.ExecContext(t.ctx, `UPDATE memo_queue SET draining=1 WHERE membership_id=? AND category_id=
			(SELECT queue_id FROM memo_delivery WHERE id=? AND drain=1)`, t.mid, delivery); err != nil {
			return res, err
		}
	}
	return res, nil
}
