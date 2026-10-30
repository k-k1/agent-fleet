-- A per-membership generation of the Bitbucket connection, shared by every Control Plane process
-- (#2709). A fetch records the generation it started under and writes its rows only if it still
-- holds, and a connect/disconnect bumps it and withdraws the cached posting capability in the same
-- transaction. 0 / no row = never changed, so existing memberships need no backfill.
CREATE TABLE IF NOT EXISTS work_item_comment_gen(
  membership_id TEXT PRIMARY KEY,
  gen           INTEGER NOT NULL DEFAULT 0
);
