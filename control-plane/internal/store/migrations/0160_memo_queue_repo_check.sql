-- Message queues, the last read of a repository's settings file (#4881 part 3).
-- memo_queue.repo_check_state is what the read found against the member's settings: differs,
-- same, invalid or absent (no file, or no entry for the category). repo_check_commit is the file's
-- commit, repo_check_diffs how many settings differ, repo_checked_at when it was read.
-- repo_check_basis names the category and the settings it was compared with: once either changes
-- the read no longer says anything and is not shown. Empty means never read, which is every
-- existing queue.
-- Forward compatible: columns are only added. A binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN repo_check_state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN repo_check_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN repo_check_diffs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN repo_check_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN repo_checked_at TEXT NOT NULL DEFAULT ''
