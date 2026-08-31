-- Scheduled execution (docs/log/38 + ADR0021), Postgres mirror of
-- migrations/0025_schedule_run_session.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule_run ADD COLUMN IF NOT EXISTS session TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_run ADD COLUMN IF NOT EXISTS trigger_kind TEXT NOT NULL DEFAULT 'scheduled'
