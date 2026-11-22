-- Schedule generation (#4834), Postgres mirror of migrations/0155_schedule_generation.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS generation INTEGER NOT NULL DEFAULT 0
