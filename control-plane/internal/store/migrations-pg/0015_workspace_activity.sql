-- Postgres mirror of migrations/0032_workspace_activity.sql.
CREATE TABLE workspace_activity (
    workspace_id    TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    last_seen_at    TEXT NOT NULL,
    connected_until TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_workspace_activity_connected ON workspace_activity(connected_until);
