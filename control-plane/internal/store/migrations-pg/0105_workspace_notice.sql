-- Postgres mirror of migrations/0120_workspace_notice.sql.
-- See the SQLite file for what the table holds and why it is forward compatible.
CREATE TABLE IF NOT EXISTS workspace_notice(
  workspace_id TEXT NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,
  raised_at    TEXT NOT NULL,
  PRIMARY KEY (workspace_id, kind)
)
