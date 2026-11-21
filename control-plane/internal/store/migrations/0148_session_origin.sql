-- Who raised a session and which session it came from (ADR 0073), mirrored so a stopped
-- Workspace's session list still lets the Console classify scheduled and auto-run runs.
-- Without the columns the relay drops both and those runs fall back into the Sessions list
-- while the Agent is down. Empty for a session with no origin. Forward compatible.
ALTER TABLE session ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE session ADD COLUMN origin_session TEXT NOT NULL DEFAULT '';
