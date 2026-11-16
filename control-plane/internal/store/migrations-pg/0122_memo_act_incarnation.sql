-- Message queues (ADR 0137 section 6), Postgres mirror of migrations/0137_memo_act_incarnation.sql. See that file.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS act_incarnation TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retire_act_incarnation TEXT NOT NULL DEFAULT '';
