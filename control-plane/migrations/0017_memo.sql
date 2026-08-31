-- Memo queue (docs/log/21): per-membership notes accumulated across devices, then flushed
-- to a coding session as one concatenated message. Grouped by repo then a free-form
-- category (sub-project). repo='' is the common/unfiled bucket. kind is 'file' (ref_path
-- points at a ~/repos path, body is an optional comment) or 'text' (body is the note).
-- An empty sent_at means unsent, a non-empty RFC3339 stamp marks a flushed memo kept
-- for the retention window (history plus re-send) before the sweep on GET removes it.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS memo(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',
  category      TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL,
  body          TEXT NOT NULL DEFAULT '',
  ref_path      TEXT NOT NULL DEFAULT '',
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  sent_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_memo_membership_repo ON memo(membership_id, repo);
