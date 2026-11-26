-- Postgres mirror of migrations/0175_session_work_link_facts.sql (#5052). Additive: new columns
-- with defaults.
ALTER TABLE session_work_link ADD COLUMN IF NOT EXISTS checks TEXT NOT NULL DEFAULT '';
ALTER TABLE session_work_link ADD COLUMN IF NOT EXISTS changes_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN IF NOT EXISTS conflict INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN IF NOT EXISTS ready INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN IF NOT EXISTS observed_at TEXT NOT NULL DEFAULT '';
