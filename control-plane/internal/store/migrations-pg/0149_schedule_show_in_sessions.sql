-- Per-schedule "Also show in the Sessions view" (issue #5070), Postgres mirror of
-- migrations/0164_schedule_show_in_sessions.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS show_in_sessions INTEGER NOT NULL DEFAULT 0
