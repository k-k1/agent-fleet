-- Message queues, step 2b (ADR 0137 section 6, issue 4203). An open delivery keeps its slot while
-- its session, or a session that session started, has something to wait for, and closing it stops
-- its session. What the Agent last reported is kept with the delivery, so that the answer does
-- not change while the workspace is stopped or the Agent is silent.
-- held_by is why the delivery was last seen held (machine, person, limit, wakers, unknown, or
-- empty when nothing held it), held_session the session it was seen in, held_seen_at when the
-- read that said so began. A write from an older read is refused, so a late answer cannot undo a
-- newer one. progress_seen_at is when a session of the tree last showed progress. reported is 1
-- once a read of an Agent that sends the queue report has been kept.
-- overdue_at is when the delivery passed its queue's max_task_hours and the member was told.
-- retire_pending is 1 from a clean close until the Agent confirms that the session was retired,
-- retired_at is when an Agent last confirmed a retire (cleared when progress is seen again).
-- turn_* copy how the first turn of the delivery's session ended, written once when it appears.
-- memo_queue.max_task_hours is the queue's bound for overdue, 1 to 72, default 12.
-- Forward compatible: columns are only added, with defaults that mean today's behaviour (nothing
-- held, not reported, not overdue, nothing to retire), and a binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN max_task_hours INTEGER NOT NULL DEFAULT 12;
ALTER TABLE memo_delivery ADD COLUMN held_by TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN held_session TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN held_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN progress_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN reported INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN overdue_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN retire_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN retired_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN turn_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN turn_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN turn_ended_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memo_delivery_retire ON memo_delivery(retire_pending) WHERE retire_pending=1
