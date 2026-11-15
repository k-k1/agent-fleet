-- Message queues, step 2b (ADR 0137), Postgres mirror of migrations/0134_memo_occupancy.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS max_task_hours INTEGER NOT NULL DEFAULT 12;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS held_by TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS held_session TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS held_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS read_gen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS held_gen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS progress_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS reported INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS overdue_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retire_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retired_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retire_result TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS turn_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS turn_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS turn_ended_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memo_delivery_retire ON memo_delivery(retire_pending) WHERE retire_pending=1
