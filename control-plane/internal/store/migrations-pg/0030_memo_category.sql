-- Memo categories (docs/log/21 UI overhaul), Postgres mirror of migrations/0020_memo_category.sql.
-- See that file for the column semantics: a memo still carries its category NAME in
-- memo.category, and this table persists the ORDER and the existence of empty categories.
--
-- ★ This is a LATE mirror, not a new feature. The sqlite side landed as 0020 and the
-- Postgres counterpart was simply never written, so on a Postgres deployment (the ECS/RDS
-- path) every category endpoint answered 500 with "relation memo_category does not exist"
-- while the memo list itself kept working — the Console folds a non-array answer into an
-- empty list, so the symptom was "categories never appear" rather than an error anybody
-- could act on. Nothing needs backfilling: memo.category has carried the names all along,
-- and a category with no row here simply has no stored order yet.
--
-- The DDL is dialect-neutral (TEXT/INTEGER), so the two files are identical apart from
-- the migration series.
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
