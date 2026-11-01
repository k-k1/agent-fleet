-- The work items and pull requests each mirrored session is linked to (#3134, #3147): the
-- Agent's session links, kept beside the session mirror so a stopped Workspace's list still
-- shows them, and so the Control Plane can say which sessions are on an item without asking
-- every Agent. Replaced with the workspace's session rows in one transaction (ReplaceSessions).
-- item_fold is item_key lower-cased: GitHub and Bitbucket owner/repo names compare without case,
-- so the key and the lookup index use the folded form while item_key keeps the spelling shown.
-- ord keeps the Agent's order (the launch item first). Forward compatible: an older Control
-- Plane never reads the table.
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
