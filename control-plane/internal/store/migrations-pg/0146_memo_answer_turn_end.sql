-- Message queues, the turn the last-answer read is pinned to, Postgres mirror of migrations/0161_memo_answer_turn_end.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS close_turn_end_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS close_turn_gen BIGINT NOT NULL DEFAULT 0
