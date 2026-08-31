-- Scheduled execution (docs/log/38), Postgres mirror of
-- migrations/0029_schedule_report.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS report INTEGER NOT NULL DEFAULT 0
