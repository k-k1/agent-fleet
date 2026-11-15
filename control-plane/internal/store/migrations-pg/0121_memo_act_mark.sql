-- Message queues, issue 4218 (ADR 0137 section 6), Postgres mirror of migrations/0136_memo_act_mark.sql. See that file.
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS act_mark BIGINT NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retire_act_mark BIGINT NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN IF NOT EXISTS retire_gen BIGINT NOT NULL DEFAULT 0
