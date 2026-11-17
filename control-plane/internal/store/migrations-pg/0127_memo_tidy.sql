-- Message queues, step 5 (ADR 0137 section 8), Postgres mirror of migrations/0142_memo_tidy.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS tidy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS tidy_destinations TEXT NOT NULL DEFAULT '[]';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS no_tidy_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS derived_from TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS source_provenance TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS tidy_from TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS tidy_seen_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS tidy_run_id TEXT NOT NULL DEFAULT '' CHECK (state <> 'superseded' OR (sent_at <> '' AND tidy_run_id <> ''));
CREATE TABLE IF NOT EXISTS memo_tidy_run(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  trigger       TEXT NOT NULL,
  state         TEXT NOT NULL,
  reason        TEXT NOT NULL DEFAULT '',
  input         TEXT NOT NULL DEFAULT '',
  ops           TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  finished_at   TEXT NOT NULL DEFAULT '',
  undone_at     TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_tidy_run_queue ON memo_tidy_run(queue_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memo_tidy_run_running ON memo_tidy_run(membership_id) WHERE state = 'running'
