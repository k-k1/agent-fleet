-- Why the Control Plane itself last stopped a workspace (#1384). The start deadline
-- (control-plane/start_deadline.go) stops a launch that never left `starting`. The member
-- gets a notification, but notifications are per membership, so without this row a tenant
-- admin sees only "stopped" and has to read the CP log to learn why.
--
-- One row per workspace, overwritten by the next automatic stop and deleted when the
-- workspace is next marked running (SetWorkspaceState), so it only ever describes the stop
-- the workspace is still in. Its own table rather than workspace columns: the SQLite 0002
-- rebuild drops every column added to workspace later (repairWorkspaceColumns).
CREATE TABLE IF NOT EXISTS workspace_auto_stop (
    workspace_id  TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    phase         TEXT NOT NULL DEFAULT '',
    limit_minutes INTEGER NOT NULL DEFAULT 0,
    stopped_at    TEXT NOT NULL
);
