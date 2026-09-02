CREATE TABLE session (
    workspace_id TEXT NOT NULL REFERENCES workspace(id),
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    dir          TEXT NOT NULL,
    repo         TEXT NOT NULL,
    label        TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    state        TEXT NOT NULL,
    last_seen    TEXT NOT NULL,
    PRIMARY KEY (workspace_id, name)
)
