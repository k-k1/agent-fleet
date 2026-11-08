-- Automatic image refresh (issue #3656). One row per workspace: the image identity (a hash
-- of the content fingerprint the runtime reports) that the Control Plane last tried to
-- restart this workspace onto by itself, and when. The row is what makes "at most one
-- automatic attempt per image" hold across CP replicas and across a failed start: the
-- claim is a single conditional upsert, so a second replica or a later sweep that sees the
-- same target gets nothing back and does not try again.
-- Its own table, like workspace_auto_stop, because the SQLite 0002 rebuild drops every
-- column added to workspace later. Starts empty, so the migration is forward compatible.
CREATE TABLE IF NOT EXISTS workspace_image_refresh (
    workspace_id TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    target       TEXT NOT NULL,
    attempted_at TEXT NOT NULL
);
