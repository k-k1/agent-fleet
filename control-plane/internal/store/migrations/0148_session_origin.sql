-- Who raised a session and which session it came from (ADR 0073), mirrored so a stopped
-- Workspace's session list still returns the lineage the Console classifies runs by.
-- Without the columns the relay drops both while the Agent is down. Empty for a session
-- with no origin. Forward compatible.
ALTER TABLE session ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE session ADD COLUMN origin_session TEXT NOT NULL DEFAULT '';
