-- Message queues, typed references (ADR 0137 section 4), Postgres mirror of migrations/0138_memo_reference.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS ref_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS ref_key TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS ref_url TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_ref_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_ref_key TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_ref_url TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR (release_ref_provider = ref_provider
  AND release_ref_key = ref_key AND release_ref_url = ref_url));
CREATE UNIQUE INDEX IF NOT EXISTS idx_memo_open_ref ON memo(membership_id, repo, category, ref_provider, lower(ref_key))
  WHERE ref_key <> '' AND state IN ('held', 'pending', 'claimed');
ALTER TABLE memo_queue ADD COLUMN IF NOT EXISTS no_refs_at TEXT NOT NULL DEFAULT ''
