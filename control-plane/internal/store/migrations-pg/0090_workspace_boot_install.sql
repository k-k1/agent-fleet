-- Postgres mirror of migrations/0105_workspace_boot_install.sql.
-- See the SQLite file for what the table holds and why it is forward compatible.
CREATE TABLE IF NOT EXISTS workspace_boot_install(
  workspace_id TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
  failed       TEXT NOT NULL DEFAULT '',
  observed_at  TEXT NOT NULL
)
