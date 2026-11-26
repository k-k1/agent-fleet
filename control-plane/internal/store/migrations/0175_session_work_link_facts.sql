-- A mirrored PR link's merge conditions as the Agent last observed them (#5052, ADR 0143
-- decision 4), so a stopped Workspace's links say what a running one says: the CI rollup
-- ('' = nothing ran, never green), changes requested, conflict, the Agent's own ready verdict
-- (0/1) and when they were observed ('' = never, which reads as not ready). Additive: new
-- columns with defaults, which an older Control Plane never reads.
ALTER TABLE session_work_link ADD COLUMN checks TEXT NOT NULL DEFAULT '';
ALTER TABLE session_work_link ADD COLUMN changes_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN conflict INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN ready INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_work_link ADD COLUMN observed_at TEXT NOT NULL DEFAULT '';
