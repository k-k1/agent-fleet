-- The schedule that started a session (origin schedule), mirrored so a stopped Workspace's
-- session list still lets the Console group scheduled sessions under their schedule.
-- Empty for any other session. Forward compatible.
ALTER TABLE session ADD COLUMN schedule_id TEXT NOT NULL DEFAULT '';
