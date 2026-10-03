-- Per-schedule delivery targets and the silent sentinel (issue #1560), Postgres mirror of
-- migrations/0085_schedule_delivery.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS deliver_to TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS silent INTEGER NOT NULL DEFAULT 0
