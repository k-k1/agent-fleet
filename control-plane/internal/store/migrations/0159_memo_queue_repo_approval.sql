-- Message queues, the approval of a repository's settings file (#4881 part 2).
-- memo_queue.repo_commit is the commit of .agent-fleet/autorun.yaml whose values the member last
-- approved for the queue, repo_approved_at when. Settings are per member, so each member's row
-- records their own approval. Empty means no approval yet, which is every existing queue.
-- Forward compatible: columns are only added. A binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN repo_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN repo_approved_at TEXT NOT NULL DEFAULT ''
