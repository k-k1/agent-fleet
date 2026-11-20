-- Message queues, a run that hands its message back, Postgres mirror of migrations/0146_memo_defer.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS not_before TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS defer_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN IF NOT EXISTS defer_reasons TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS defer_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS defer_not_before TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS defer_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS defer_result TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS return_blocked TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS returned_at TEXT NOT NULL DEFAULT ''
