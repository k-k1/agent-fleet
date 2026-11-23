package store

import "context"

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
	// session and no answer decided, last tried before before (never tried first), at most limit:
	// with waited, those a pending message of the same bucket waits for, else the others.
	ListAnswersDue(ctx context.Context, since, before string, waited bool, limit int) ([]MemoDelivery, error)
	// MarkAnswerTried stamps a delivery whose answer is about to be read.
	MarkAnswerTried(ctx context.Context, id, at string) error
	// RecordAnswer keeps a read's answer on a closed delivery that has none and closed at or after
	// since: written once, so a later turn of the same session (a person taking it over) never
	// rewrites what the run ended with, and never after the window to read it ended. False means
	// nothing was written.
	RecordAnswer(ctx context.Context, id, head, class, since string) (bool, error)
}

// memoDeliveryWaitedSQL holds for a delivery row that a pending message of the same bucket as the
// message it delivered names as a prerequisite.
const memoDeliveryWaitedSQL = `EXISTS (SELECT 1 FROM memo p JOIN memo m ON m.membership_id=p.membership_id AND m.repo=p.repo AND m.category=p.category
	  WHERE p.membership_id=memo_delivery.membership_id AND p.delivery_id=memo_delivery.id AND p.state='delivered'
	    AND m.state='pending' AND m.after_ids LIKE '%"' || p.id || '"%')`

func (s *SQL) ListAnswersDue(ctx context.Context, since, before string, waited bool, limit int) ([]MemoDelivery, error) {
	cond := memoDeliveryWaitedSQL
	if !waited {
		cond = `NOT ` + cond
	}
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE state=? AND session<>'' AND answer_class=''
		  AND closed_at>=? AND answer_tried_at<? AND `+cond+`
		ORDER BY answer_tried_at, closed_at, id LIMIT ?`, DeliveryClosed, since, before, limit))
}

func (s *SQL) MarkAnswerTried(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_delivery SET answer_tried_at=? WHERE id=?`, at, id)
	return err
}

func (s *SQL) RecordAnswer(ctx context.Context, id, head, class, since string) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET answer_head=?, answer_class=?
		WHERE id=? AND state=? AND answer_class='' AND closed_at>=?`, head, class, id, DeliveryClosed, since))
}
