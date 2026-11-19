-- Message queues: name what holds a run open, Postgres mirror of migrations/0143_memo_held_detail.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS held_detail TEXT NOT NULL DEFAULT ''
