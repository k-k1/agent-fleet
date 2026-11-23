-- Postgres mirror of migrations/0160_memo_queue_repo_check.sql. The migrator splits on the semicolon, so comments must not contain one.
-- Message queues, the last read of a repository's settings file (#4881 part 3). Empty means never read.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_check_state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_check_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_check_diffs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_check_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_checked_at TEXT NOT NULL DEFAULT ''
