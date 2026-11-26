-- When the Agent first saw a mirrored session stopped (RFC3339, #5155), so a stopped Workspace's
-- list can still tell the Console's Active view how recent a stop is. '' for a live session and
-- for one the Agent reported without it. Additive: a new column with a default.
ALTER TABLE session ADD COLUMN stopped_at TEXT NOT NULL DEFAULT '';
