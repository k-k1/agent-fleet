-- Message queues, the last answer of a delivery's root session, Postgres mirror of migrations/0158_memo_answer_head.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS answer_head TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS answer_class TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS answer_tried_at TEXT NOT NULL DEFAULT ''
