-- The message-queue category whose delivery started a session (origin queue), mirrored so a
-- stopped Workspace's session list still lets the Console group queue runs under their category.
-- Empty for any other session. Forward compatible.
ALTER TABLE session ADD COLUMN queue_category_id TEXT NOT NULL DEFAULT '';
