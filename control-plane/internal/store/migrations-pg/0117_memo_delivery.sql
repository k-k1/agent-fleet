-- Message queues, step 1c (ADR 0137), Postgres mirror of migrations/0132_memo_delivery.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS memo_delivery(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  state         TEXT NOT NULL,
  outcome       TEXT NOT NULL DEFAULT '',
  session       TEXT NOT NULL DEFAULT '',
  message_ids   TEXT NOT NULL DEFAULT '[]',
  idle_ticks    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  sent_at       TEXT NOT NULL DEFAULT '',
  opened_at     TEXT NOT NULL DEFAULT '',
  closed_at     TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_ms ON memo_delivery(membership_id, state);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_queue ON memo_delivery(queue_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_state ON memo_delivery(state, created_at);
ALTER TABLE memo ADD COLUMN IF NOT EXISTS delivery_id TEXT NOT NULL DEFAULT ''
