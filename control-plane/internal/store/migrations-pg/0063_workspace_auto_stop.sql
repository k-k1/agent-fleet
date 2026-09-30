-- Postgres mirror of migrations/0078_workspace_auto_stop.sql.
CREATE TABLE IF NOT EXISTS workspace_auto_stop (
    workspace_id  TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    phase         TEXT NOT NULL DEFAULT '',
    limit_minutes INTEGER NOT NULL DEFAULT 0,
    stopped_at    TEXT NOT NULL
);
