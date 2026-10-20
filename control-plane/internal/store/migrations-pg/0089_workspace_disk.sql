-- Postgres mirror of migrations/0104_workspace_disk.sql.
-- See the SQLite file for what the table holds and why it is forward compatible.
CREATE TABLE IF NOT EXISTS workspace_disk(
  workspace_id TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
  used         BIGINT NOT NULL,
  total        BIGINT NOT NULL DEFAULT 0,
  observed_at  TEXT NOT NULL,
  cleared      INTEGER NOT NULL DEFAULT 0
)
