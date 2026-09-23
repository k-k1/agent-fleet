-- The image studio a session is bound to (ADR 0100 decision 2), mirrored so a stopped
-- Workspace's session list still says which session belongs to which studio. Without the
-- column the relay drops it and every studio session reads as an ordinary one while the
-- Agent is down. Empty for a session with no studio. Forward compatible: a new column
-- with a default, which an older Control Plane simply never reads.
ALTER TABLE session ADD COLUMN studio TEXT NOT NULL DEFAULT '';
