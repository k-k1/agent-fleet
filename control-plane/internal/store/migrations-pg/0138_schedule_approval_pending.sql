-- Schedule approval hold (#4631), Postgres mirror of migrations/0153_schedule_approval_pending.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS approval_pending INTEGER NOT NULL DEFAULT 0
