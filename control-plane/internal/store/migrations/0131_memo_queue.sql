-- Message queues, step 1a (ADR 0137 sections 1 and 3, issue 4156). A memo becomes a message: state
-- is empty for a plain note (every existing row) or held, pending, claimed, delivered, unconfirmed
-- or superseded for a message in a queue. provenance is the face a create arrived by (member,
-- workspace, mcp, inbox, curator), written by the CP and never changed, with empty read as
-- workspace. producer and producer_origin are attribution only. release_hash is the content hash
-- the member released, and a pending message's release_hash always equals its current content hash.
-- memo_queue holds the settings that make a memo_category row a queue, keyed by that row's id.
-- Forward compatible: columns and a table are only added, with defaults that mean today's
-- behaviour, and a binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN provenance TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN producer TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN producer_origin TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN released_at TEXT NOT NULL DEFAULT '';
UPDATE memo SET provenance='inbox' WHERE origin_item_id<>'' AND provenance='';
CREATE TABLE IF NOT EXISTS memo_queue(
  category_id   TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  instruction   TEXT NOT NULL DEFAULT '',
  agent_kind    TEXT NOT NULL DEFAULT 'claude',
  model         TEXT NOT NULL DEFAULT '',
  effort        TEXT NOT NULL DEFAULT '',
  route         TEXT NOT NULL DEFAULT '',
  spend_cap_usd REAL NOT NULL DEFAULT 0,
  worktree      INTEGER NOT NULL DEFAULT 1,
  max_active    INTEGER NOT NULL DEFAULT 2,
  batch         INTEGER NOT NULL DEFAULT 1,
  enabled       INTEGER NOT NULL DEFAULT 0,
  generation    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_queue_ms ON memo_queue(membership_id)
