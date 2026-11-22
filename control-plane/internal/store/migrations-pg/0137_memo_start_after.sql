-- Message queues, a start time a message carries, Postgres mirror of migrations/0152_memo_start_after.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS start_after TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_start_after TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_start_after = start_after)
