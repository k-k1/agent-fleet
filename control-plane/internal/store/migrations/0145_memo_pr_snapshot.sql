-- Message queues, the PR snapshot of a prerequisite's delivery (ADR 0137, addendum 2026-11-19,
-- part A, step 2). A message that waits for another is satisfied only once every PR of the
-- prerequisite's session is merged, read from the running Agent after the delivery closed, never
-- from the session mirror.
-- pr_snapshot is the JSON array of those PRs with their states, pr_state what they sum up to:
-- empty until a read answered, then none (no PR), waiting, merged, closed (one closed unmerged) or
-- gone (the Agent no longer had the session before any read answered). none, merged, closed and
-- gone are final and never asked again. pr_read_at is the last read that answered, pr_tried_at the
-- last read begun (the 10-minute pace), pr_changed_at when the snapshot last changed (the 24 hours
-- after which the wakes for it stop).
-- memo_pr_wake is a wake of a stopped workspace made only to read a snapshot, per member. It is
-- not a delivery, so the fires of the day and the settle wakes never count it.
-- Forward compatible: columns with defaults and a new table. A binary that predates them ignores
-- them and decides a prerequisite by step 1 alone, as it did.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN pr_snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN pr_state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN pr_read_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN pr_tried_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN pr_changed_at TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS memo_pr_wake(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_pr_wake_ms ON memo_pr_wake(membership_id, created_at)
