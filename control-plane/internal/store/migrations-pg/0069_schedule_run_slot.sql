-- Scheduled runs dropped before they ran (issue #1257), Postgres mirror of
-- migrations/0084_schedule_run_slot.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule_run ADD COLUMN IF NOT EXISTS slot TEXT NOT NULL DEFAULT ''
