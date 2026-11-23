-- Postgres mirror of migrations/0158_memo_queue_repo_approval.sql. The migrator splits on the semicolon, so comments must not contain one.
-- Message queues, the approval of a repository's settings file (#4881 part 2).
-- memo_queue.repo_commit is the commit of .agent-fleet/autorun.yaml whose values the member last
-- approved for the queue, repo_approved_at when. Empty means no approval yet.
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS repo_approved_at TEXT NOT NULL DEFAULT ''
