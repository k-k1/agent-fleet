package store

// Dependencies between messages (ADR 0137, addendum 2026-11-19, part A, step 1). A message names
// the messages of its queue it waits for (memo.after_ids), and it may be claimed only once every
// one of them is satisfied. The rule is written twice, once in SQL for the dispatcher's
// statistics (memoEligibleSQL) and once in Go for the claim, the hold and the display
// (AfterDeliverySatisfies): the two must agree, or a threshold opens on messages the claim then
// skips. TestMemoAfterSQLAgreesWithGo pins them together.

// MemoQuickTurnMs is the "ended within a minute" bound of a first turn, in milliseconds: a turn
// with no clean end that ended sooner counts as bad (the breaker of ADR 0137 section 7).
const MemoQuickTurnMs = 60_000

// TurnBad reports whether a delivery's first turn counts as bad: it failed or was aborted, or it
// ended within a minute without a clean end. A turn with no recorded outcome counts by the minute
// alone. It says nothing about a delivery whose first turn is not recorded (TurnEndedAt "").
func TurnBad(d MemoDelivery) bool {
	switch d.TurnOutcome {
	case "turn-failed", "turn-aborted":
		return true
	case "clean":
		return false
	}
	return d.TurnMs < MemoQuickTurnMs
}

// RetiredOverdue reports whether a delivery's tree was retired as overdue: it passed its
// max_task_hours and the overdue retire's condition was frozen, with no progress seen since
// (progress clears retire_gen). A clean close's own retire never sets retire_gen.
func RetiredOverdue(d MemoDelivery) bool { return d.OverdueAt != "" && d.RetireGen != 0 }

// AfterDeliverySatisfies is step 1 of "satisfied": the delivery that delivered a prerequisite
// closed as clean, its first turn is recorded and was not bad, and it was not retired as overdue.
// The prerequisite itself must be delivered by that delivery (not sent by hand), which the caller
// checks.
func AfterDeliverySatisfies(d MemoDelivery) bool {
	return d.State == DeliveryClosed && d.Outcome == DeliveryClean && d.TurnEndedAt != "" && !TurnBad(d) && !RetiredOverdue(d)
}

// memoPrereqSatisfiedSQL is AfterDeliverySatisfies, plus "delivered by a delivery", for a
// prerequisite row p.
const memoPrereqSatisfiedSQL = `p.state='delivered' AND p.delivery_id<>'' AND EXISTS (SELECT 1 FROM memo_delivery d
	  WHERE d.id=p.delivery_id AND d.state='closed' AND d.outcome='clean' AND d.turn_ended_at<>''
	    AND d.turn_outcome NOT IN ('turn-failed','turn-aborted') AND (d.turn_outcome='clean' OR d.turn_ms>=60000)
	    AND NOT (d.overdue_at<>'' AND d.retire_gen<>0))`

// memoEligibleSQL holds for a pending row m whose prerequisites are all satisfied. after_ids is a
// JSON array of hex ids with no duplicate (the API refuses one), so its element count is its
// commas plus one, and an id is found in it by its quoted form. Only rows of m's own bucket count:
// a prerequisite moved out of the queue is not satisfied.
const memoEligibleSQL = `(m.after_ids='' OR (SELECT COUNT(*) FROM memo p WHERE p.membership_id=m.membership_id
	  AND p.repo=m.repo AND p.category=m.category AND m.after_ids LIKE '%"' || p.id || '"%' AND ` + memoPrereqSatisfiedSQL + `)
	  = LENGTH(m.after_ids) - LENGTH(REPLACE(m.after_ids, ',', '')) + 1)`
