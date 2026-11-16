-- Message queues, step 4 (ADR 0137 section 3), Postgres mirror of migrations/0139_memo_accept_own_sessions.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS accept_own_sessions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN IF NOT EXISTS released_by TEXT NOT NULL DEFAULT ''
