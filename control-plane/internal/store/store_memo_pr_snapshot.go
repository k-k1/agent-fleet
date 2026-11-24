package store

import (
	"context"
	"time"
)

// The PR snapshot of a prerequisite's delivery (ADR 0137, addendum 2026-11-19, part A, step 2):
// the PRs of the delivery's session and their states, read from the running Agent after the
// delivery closed and kept on the delivery. The dispatcher reads it again at most every 10
// minutes while a message waits for it, until it is final.

// The values of memo_delivery.pr_state. "" is no answer yet. PRStateNone, PRStateMerged,
// PRStateClosed and PRStateGone are final: such a snapshot is never asked again.
const (
	PRStateNone    = "none"    // the session has no PR, read after the close
	PRStateWaiting = "waiting" // a PR is open, or its state is unknown
	PRStateMerged  = "merged"  // every PR is merged, and there is at least one
	PRStateClosed  = "closed"  // a PR was closed unmerged
	PRStateGone    = "gone"    // the Agent no longer had the session before any snapshot
)

// PRStateFinal reports whether a snapshot in state s is never asked again.
func PRStateFinal(s string) bool {
	switch s {
	case PRStateNone, PRStateMerged, PRStateClosed, PRStateGone:
		return true
	}
	return false
}

// MemoPRSnapshotStore is what the dispatcher's snapshot pass reads and writes, outside any
// membership lock: no slot and no message is written by it.
type MemoPRSnapshotStore interface {
	// ListPRSnapshotsDue returns, of every member but those in except, the closed clean deliveries
	// a pending message waits for whose snapshot is not final and was last tried before before
	// (never tried first), at most limit, in (pr_tried_at, closed_at, id) order and past after when
	// it is given (the last row of the page before). A delivery that can no longer satisfy step 1
	// (retired as overdue, a bad first turn) is left out: its dependents go to held whatever its
	// PRs say.
	ListPRSnapshotsDue(ctx context.Context, before string, after *MemoDelivery, except []string, limit int) ([]MemoDelivery, error)
	// ListPRSnapshotStoppedMembers returns the members with a row ListPRSnapshotsDue would list
	// whose workspace is recorded as stopped, at most limit (memoDueStoppedMembersSQL).
	ListPRSnapshotStoppedMembers(ctx context.Context, before string, limit int) ([]string, error)
	// MarkPRTried stamps a delivery whose snapshot is about to be asked.
	MarkPRTried(ctx context.Context, id, at string) error
	// RecordPRSnapshot keeps an answer of the Agent: the PRs and what they sum up to. It lands
	// only while the stored snapshot is not final, and a snapshot of none only while no answer was
	// kept before (a read that found PRs is never undone by one that found none). changed_at moves
	// when the PRs or their states differ from the stored ones. False means nothing was written.
	RecordPRSnapshot(ctx context.Context, id, snapshot, state, at string) (bool, error)
	// RecordPRGone records that the Agent no longer has the session, only while no answer was
	// kept: a snapshot that names PRs is asked by number and needs no session.
	RecordPRGone(ctx context.Context, id, at string) (bool, error)
}

// MemoPRWakeTxOps are the wakes made only to read a snapshot, per member. They are not deliveries:
// CountDeliveriesCreatedSince and CountMemberWakesSince never see them.
type MemoPRWakeTxOps interface {
	CountPRWakesSince(since string) (int, error)
	// InsertPRWake records one, and forgets the member's wakes older than two days.
	InsertPRWake(id, queueID, at string) error
}

// memoPRSnapshotsDueSQL is ListPRSnapshotsDue's condition and its args.
func memoPRSnapshotsDueSQL(before string) (string, []any) {
	return `state=? AND outcome=? AND session<>''
		  AND pr_state IN ('',?) AND pr_tried_at<?
		  AND NOT (overdue_at<>'' AND retire_gen<>0)
		  AND NOT (turn_ended_at<>'' AND (turn_outcome IN ('turn-failed','turn-aborted') OR (turn_outcome<>'clean' AND turn_ms<?)))
		  AND EXISTS (SELECT 1 FROM memo p JOIN memo m ON m.membership_id=p.membership_id AND m.repo=p.repo AND m.category=p.category
		    WHERE p.membership_id=memo_delivery.membership_id AND p.delivery_id=memo_delivery.id AND p.state=?
		      AND m.state=? AND m.after_ids LIKE '%"' || p.id || '"%')`,
		[]any{DeliveryClosed, DeliveryClean, PRStateWaiting, before, MemoQuickTurnMs, MemoStateDelivered, MemoStatePending}
}

func (s *SQL) ListPRSnapshotsDue(ctx context.Context, before string, after *MemoDelivery, except []string, limit int) ([]MemoDelivery, error) {
	cond, args := memoPRSnapshotsDueSQL(before)
	page, pargs := memoDeliveryPageSQL("pr_tried_at", func(d MemoDelivery) string { return d.PRTriedAt }, after, except)
	args = append(append(args, pargs...), limit)
	return scanMemoDeliveries(s.db.QueryContext(ctx, memoDeliveryCols+` WHERE `+cond+page+`
		ORDER BY pr_tried_at, closed_at, id LIMIT ?`, args...))
}

func (s *SQL) ListPRSnapshotStoppedMembers(ctx context.Context, before string, limit int) ([]string, error) {
	cond, args := memoPRSnapshotsDueSQL(before)
	q, args := memoDueStoppedMembersSQL("pr_tried_at", cond, args, limit)
	return scanMembershipIDs(s.db.QueryContext(ctx, q, args...))
}

func (s *SQL) MarkPRTried(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_delivery SET pr_tried_at=? WHERE id=?`, at, id)
	return err
}

func (s *SQL) RecordPRSnapshot(ctx context.Context, id, snapshot, state, at string) (bool, error) {
	args := []any{snapshot, state, at, snapshot, at, id, DeliveryClosed}
	guard := `pr_state=''`
	if state != PRStateNone {
		guard = `pr_state IN ('',?)`
		args = append(args, PRStateWaiting)
	}
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET pr_snapshot=?, pr_state=?, pr_read_at=?,
		  pr_changed_at=CASE WHEN pr_changed_at='' OR pr_snapshot<>? THEN ? ELSE pr_changed_at END
		WHERE id=? AND state=? AND `+guard, args...))
}

func (s *SQL) RecordPRGone(ctx context.Context, id, at string) (bool, error) {
	return affected(s.db.ExecContext(ctx, `UPDATE memo_delivery SET pr_state=?, pr_read_at=?, pr_changed_at=?
		WHERE id=? AND state=? AND pr_state=''`, PRStateGone, at, at, id, DeliveryClosed))
}

func (t memoTx) CountPRWakesSince(since string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_pr_wake WHERE membership_id=? AND created_at>=?`,
		t.mid, since).Scan(&n)
	return n, err
}

func (t memoTx) InsertPRWake(id, queueID, at string) error {
	if _, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_pr_wake(id, membership_id, queue_id, created_at) VALUES(?,?,?,?)`,
		id, t.mid, queueID, at); err != nil {
		return err
	}
	at2, err := time.Parse("2006-01-02T15:04:05.000000Z", at)
	if err != nil {
		return nil
	}
	_, err = t.q.ExecContext(t.ctx, `DELETE FROM memo_pr_wake WHERE membership_id=? AND created_at<?`,
		t.mid, DeliveryTS(at2.Add(-48*time.Hour)))
	return err
}
