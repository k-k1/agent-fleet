-- Cross-replica watermark for "a human typed in this session terminal". The terminal
-- WebSocket lives on one CP replica, and tier 1 of the idle reaper may run on another,
-- which reads this row before halting a session. One monotonic row per session.
-- Forward compatible: a new table only, never touched by replicas that predate it.
CREATE TABLE workspace_session_input (
    workspace_id  TEXT NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    session       TEXT NOT NULL,
    last_input_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, session)
);
