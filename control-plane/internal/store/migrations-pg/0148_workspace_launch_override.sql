-- The tenant-wide boost hours read, Postgres mirror of migrations/0163_workspace_launch_override.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE INDEX IF NOT EXISTS idx_workspace_launch_override ON workspace_launch(override, started_at)
