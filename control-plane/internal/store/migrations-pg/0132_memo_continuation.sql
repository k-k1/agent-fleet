-- Message queues, a bounded continuation of a released message (ADR 0137, addendum 2026-11-20, #4606).
-- The same columns and table as migrations/0147_memo_continuation.sql. Forward compatible.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS continuation_max INTEGER NOT NULL DEFAULT 4;
ALTER TABLE memo ADD COLUMN IF NOT EXISTS chain_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS chain_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN IF NOT EXISTS chain_delivery TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS memo_chain(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  category_id   TEXT NOT NULL,
  ref_provider  TEXT NOT NULL DEFAULT '',
  ref_key       TEXT NOT NULL DEFAULT '',
  admitted      INTEGER NOT NULL DEFAULT 0,
  stopped       INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
)
