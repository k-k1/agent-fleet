-- Scheduled execution x stop-after-turn (docs/log/85), Postgres mirror of
-- migrations/0054_schedule_stop_after_run.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS stop_after_run INTEGER NOT NULL DEFAULT 0
