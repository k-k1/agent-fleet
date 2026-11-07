-- Postgres mirror of migrations/0124_workspace_session_input.sql.
CREATE TABLE workspace_session_input (
    workspace_id  TEXT NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    session       TEXT NOT NULL,
    last_input_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, session)
);
