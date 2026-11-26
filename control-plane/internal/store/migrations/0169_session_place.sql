-- Where a mirrored session runs (a repository, a worktree, a space or home), as the Agent stored it
-- at create (#5219): a JSON object {kind, id, parent}, so a stopped Workspace's list still tells a
-- space from the repository that shares its folder name. NULL for a session the Agent reported
-- without one (created before the Agent stored it). Additive: a new nullable column.
ALTER TABLE session ADD COLUMN place TEXT;
