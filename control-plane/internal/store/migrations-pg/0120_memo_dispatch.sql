-- Message queues, step 3 (ADR 0137), Postgres mirror of migrations/0135_memo_dispatch.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS open_cron TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS open_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS tz TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS open_after INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS max_fires_per_day INTEGER NOT NULL DEFAULT 20;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS draining INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS paused_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS paused_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS breaker_after TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS no_report_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS auto INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS limit_kinds TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memo_delivery_turn ON memo_delivery(queue_id, created_at) WHERE turn_ended_at <> ''
