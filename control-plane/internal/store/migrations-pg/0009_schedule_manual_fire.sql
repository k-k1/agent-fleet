-- Scheduled execution (docs/log/38 + ADR0021), Postgres mirror of
-- migrations/0026_schedule_manual_fire.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS manual_fire_pending INTEGER NOT NULL DEFAULT 0
