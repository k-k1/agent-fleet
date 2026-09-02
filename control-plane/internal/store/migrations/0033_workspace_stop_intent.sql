-- Atomic boundary between idle-stop and new cross-replica activity.
CREATE TABLE workspace_stop_intent (
    workspace_id        TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    owner_membership_id TEXT NOT NULL REFERENCES membership(id) ON DELETE CASCADE,
    operation_id        TEXT NOT NULL,
    created_at          TEXT NOT NULL
);
