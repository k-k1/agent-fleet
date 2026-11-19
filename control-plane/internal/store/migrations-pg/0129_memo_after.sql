-- Message queues, dependencies between messages, Postgres mirror of migrations/0144_memo_after.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS after_ids TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS held_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_after_ids TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_after_ids = after_ids)
