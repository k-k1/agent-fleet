-- Message queues, the "run next" mark of a waiting message, Postgres mirror of migrations/0162_memo_run_next.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS run_next INTEGER NOT NULL DEFAULT 0
