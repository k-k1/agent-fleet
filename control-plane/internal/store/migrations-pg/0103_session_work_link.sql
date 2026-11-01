-- Postgres mirror of migrations/0118_session_work_link.sql (#3147).
-- See the SQLite file for what the table means.
CREATE TABLE IF NOT EXISTS session_work_link(
  workspace_id TEXT NOT NULL,
  session_name TEXT NOT NULL,
  ord INTEGER NOT NULL,
  provider TEXT NOT NULL,
  item_key TEXT NOT NULL,
  item_fold TEXT NOT NULL,
  role TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  linked_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (workspace_id, session_name, provider, item_fold)
);
CREATE INDEX IF NOT EXISTS idx_session_work_link_item ON session_work_link(provider, item_fold)
