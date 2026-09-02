-- Scheduled execution P6 (docs/log/38 + ADR0021), Postgres mirror of migrations/0024_schedule_reuse.sql.
-- See that file for the column semantics. DDL is dialect-neutral here (TEXT/INTEGER),
-- so the two stay identical apart from living in the pg migration series.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS reuse_session TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS reuse_started_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS reuse_run_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS rotation TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS missing_target_policy TEXT NOT NULL DEFAULT 'recreate';
