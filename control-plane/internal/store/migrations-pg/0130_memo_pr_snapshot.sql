-- Message queues, the PR snapshot of a prerequisite's delivery, Postgres mirror of migrations/0145_memo_pr_snapshot.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS pr_snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS pr_state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS pr_read_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS pr_tried_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS pr_changed_at TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS memo_pr_wake(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_pr_wake_ms ON memo_pr_wake(membership_id, created_at)
