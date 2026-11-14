-- Message queues, step 1a (ADR 0137), Postgres mirror of migrations/0131_memo_queue.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS provenance TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS producer TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS producer_origin TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS released_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_repo TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_body TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_ref_path TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_attachments TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_category TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR (release_repo = repo
  AND release_category = category AND release_kind = kind AND release_body = body AND release_ref_path = ref_path
  AND release_attachments = attachments));
UPDATE memo SET provenance='inbox' WHERE origin_item_id<>'' AND provenance='';
CREATE TABLE IF NOT EXISTS memo_queue(
  category_id   TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  instruction   TEXT NOT NULL DEFAULT '',
  agent_kind    TEXT NOT NULL DEFAULT 'claude',
  model         TEXT NOT NULL DEFAULT '',
  effort        TEXT NOT NULL DEFAULT '',
  route         TEXT NOT NULL DEFAULT '',
  spend_cap_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
  worktree      INTEGER NOT NULL DEFAULT 1,
  max_active    INTEGER NOT NULL DEFAULT 2,
  batch         INTEGER NOT NULL DEFAULT 1,
  enabled       INTEGER NOT NULL DEFAULT 0,
  generation    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_queue_ms ON memo_queue(membership_id);
CREATE TABLE IF NOT EXISTS memo_queue_intake(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_queue_intake_ms ON memo_queue_intake(membership_id, created_at)
