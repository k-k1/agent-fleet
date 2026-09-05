-- Memo categories (docs/log/21 UI overhaul): categories become first-class so they can be added
-- ahead of any memo, reordered by drag-and-drop, and kept even while empty. Scoped per
-- membership then repo (repo='' is the common bucket), mirroring the memo table. A memo
-- still carries its category NAME in memo.category — this table persists the ORDER and the
-- existence of empty categories. name is unique within a (membership, repo) so the name
-- stays the grouping key. position orders the categories inside a repo bucket.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS memo_category(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',
  name          TEXT NOT NULL,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_memo_category_name ON memo_category(membership_id, repo, name);
CREATE INDEX IF NOT EXISTS idx_memo_category_ms ON memo_category(membership_id, repo);
