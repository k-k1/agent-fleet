package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The last answer of a delivery's root session (ADR 0137, issue #4876): the first line of what the
// session said last, read from the running Agent after the delivery closed and kept on the
// delivery. A run that opened no PR satisfies a dependent's `after` only when it reads as done;
// the run history shows it for every run.

// The values of memo_delivery.answer_class. "" is no read decided yet; every other value is final.
const (
	AnswerDone    = "done"    // starts with Done:
	AnswerPartial = "partial" // starts with Partial:, Held:, Deferred:, Too large: or Stopped at part
	AnswerOther   = "other"   // any other first line
	AnswerUnknown = "unknown" // the session, its transcript or an answer in it could not be read
)

// AnswerHeadMax bounds memo_delivery.answer_head, in runes.
const AnswerHeadMax = 200

// MemoAnswerStore is what the dispatcher's answer pass reads and writes, outside any membership
// lock: no slot and no message is written by it.
type MemoAnswerStore interface {
	// ListAnswersDue returns, of every member, the deliveries closed at or after since with a
	// session and no answer decided, last tried before before (never tried first), at most limit,
	// leaving skip out: each member's in (answer_tried_at, closed_at, id) order, the members'
	// first rows before any second (memoDueRankedSQL). With waited, those a pending message of the
	// same bucket waits for, else the others.
	ListAnswersDue(ctx context.Context, since, before string, waited bool, skip MemoDueSkip, limit int) ([]MemoDelivery, error)
	// MarkAnswerTried stamps a delivery whose answer is about to be read.
	MarkAnswerTried(ctx context.Context, id, at string) error
	// RecordAnswer keeps a read's answer on a closed delivery that has none and closed at or after
	// since: written once, so a later turn of the same session (a person taking it over) never
	// rewrites what the run ended with, and never after the window to read it ended: the close is
	// at or after since (the caller's clock) and no more than window before the database's clock
	// when the row is written. False means nothing was written.
	RecordAnswer(ctx context.Context, id, head, class, since string, window time.Duration) (bool, error)
}

// memoDeliveryWaitedSQL holds for a delivery row that a pending message of the same bucket as the
// message it delivered names as a prerequisite.
const memoDeliveryWaitedSQL = `EXISTS (SELECT 1 FROM memo p JOIN memo m ON m.membership_id=p.membership_id AND m.repo=p.repo AND m.category=p.category
	  WHERE p.membership_id=memo_delivery.membership_id AND p.delivery_id=memo_delivery.id AND p.state='delivered'
	    AND m.state='pending' AND m.after_ids LIKE '%"' || p.id || '"%')`

// MemoDueSkip is what a pass's next page of a due list leaves out: the members whose rows the
// pass leaves unstamped (a stopped workspace), and the rows whose stamp it could not write. Every
// row a page returns is stamped or named here, so the next page goes on from the rows left.
type MemoDueSkip struct {
	Members []string
	IDs     []string
}

// memoDueRankedSQL lists the ids of the deliveries that match cond (its args), each ranked within
// its member in (tried, closed_at, id) order, and leaves skip out. The list is ordered by that
// rank first: every member's first row comes before any member's second, so a member whose rows
// a pass leaves unstamped costs one row of a page however many rows it has, and a page bound is
// spent on distinct members.
func memoDueRankedSQL(tried, cond string, args []any, skip MemoDueSkip, limit int) (string, []any) {
	for _, list := range []struct {
		col string
		ids []string
	}{{"membership_id", skip.Members}, {"id", skip.IDs}} {
		if len(list.ids) == 0 {
			continue
		}
		cond += ` AND ` + list.col + ` NOT IN (` + inList(len(list.ids)) + `)`
		for _, v := range list.ids {
			args = append(args, v)
		}
	}
	args = append(args, limit)
	return memoDeliveryCols + ` JOIN (SELECT id AS ranked_id, ROW_NUMBER() OVER (PARTITION BY membership_id ORDER BY ` + tried + `, closed_at, id) AS member_rank
		  FROM memo_delivery WHERE ` + cond + `) ranked ON ranked.ranked_id=memo_delivery.id
		ORDER BY ranked.member_rank, ` + tried + `, closed_at, id LIMIT ?`, args
}

func (s *SQL) ListAnswersDue(ctx context.Context, since, before string, waited bool, skip MemoDueSkip, limit int) ([]MemoDelivery, error) {
	waitedCond := memoDeliveryWaitedSQL
	if !waited {
		waitedCond = `NOT ` + waitedCond
	}
	q, args := memoDueRankedSQL("answer_tried_at", `state=? AND session<>'' AND answer_class=''
		  AND closed_at>=? AND answer_tried_at<? AND `+waitedCond, []any{DeliveryClosed, since, before}, skip, limit)
	return scanMemoDeliveries(s.db.QueryContext(ctx, q, args...))
}

func (s *SQL) MarkAnswerTried(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_delivery SET answer_tried_at=? WHERE id=?`, at, id)
	return err
}

// RecordAnswer judges the window by both clocks: since alone was computed before the statement, so
// an UPDATE that waited on a row lock past the edge would still keep an answer. The database's
// clock is read once the row is held: SQLite takes its write lock before it reads 'now', and on
// Postgres the row is locked before the UPDATE.
func (s *SQL) RecordAnswer(ctx context.Context, id, head, class, since string, window time.Duration) (bool, error) {
	q := `UPDATE memo_delivery SET answer_head=?, answer_class=?
		WHERE id=? AND state=? AND answer_class='' AND closed_at>=? AND closed_at>=` + s.dbDeliveryTSAgo(window)
	args := []any{head, class, id, DeliveryClosed, since}
	if s.dialect != "postgres" {
		return affected(s.db.ExecContext(ctx, q, args...))
	}
	// A plain UPDATE that waits on a row only locked (FOR UPDATE, or a write rolled back) goes on
	// without re-checking its WHERE, with the clock it read before the wait. The row is locked
	// first, so the UPDATE reads the clock once the row is this transaction's.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memo_delivery WHERE id=? FOR UPDATE`, id).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	ok, err := affected(tx.ExecContext(ctx, q, args...))
	if err != nil {
		return false, err
	}
	return ok, tx.Commit()
}

// dbDeliveryTSAgo is an SQL expression for the database's clock less d, in DeliveryTS's form
// (microseconds, UTC, Z), so it compares as text with the stamps the Control Plane writes. d is
// taken in whole seconds.
func (s *SQL) dbDeliveryTSAgo(d time.Duration) string {
	secs := int64(d / time.Second)
	if s.dialect == "postgres" {
		return fmt.Sprintf(`to_char((clock_timestamp() AT TIME ZONE 'UTC') - interval '%d seconds', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`, secs)
	}
	// strftime's %f is seconds with milliseconds: SQLite's clock has no finer grain.
	return fmt.Sprintf(`(strftime('%%Y-%%m-%%dT%%H:%%M:%%f', 'now', '-%d seconds') || '000Z')`, secs)
}
