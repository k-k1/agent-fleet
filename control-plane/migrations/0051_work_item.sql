-- Work item inbox (docs/80 / ADR 0061): external tickets (GitHub Issue / PR, later Jira)
-- listed in the left rail so a session can be started from one. The CP owns the saved
-- queries and a cache of NON-SECRET metadata ONLY -- never the description, comments or
-- provider tokens (ADR 0061 decision 2). The cache exists so the rail still renders while
-- the Workspace is stopped, which is exactly when a ticket is picked.
-- fetched_at is the evidence behind the mandatory "last fetched hh:mm" label and doubles
-- as the refresh rate limiter (the SSE tick is 4s -- without it every tick would hit the
-- provider API).
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS work_item_query(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  provider      TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  query         TEXT NOT NULL,
  repo_hint     TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  fetched_at    TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_work_item_query_membership ON work_item_query(membership_id);
CREATE TABLE IF NOT EXISTS work_item_cache(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  query_id      TEXT NOT NULL,
  provider      TEXT NOT NULL,
  item_kind     TEXT NOT NULL DEFAULT 'issue',
  item_key      TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'open',
  url           TEXT NOT NULL DEFAULT '',
  assignee      TEXT NOT NULL DEFAULT '',
  labels        TEXT NOT NULL DEFAULT '',
  repo          TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL DEFAULT '',
  fetched_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_item_cache_membership ON work_item_cache(membership_id);
-- The ledger of "this ticket has been started". It deliberately has NO foreign key to
-- work_item_cache: the cache is a volatile query result, but the fact that someone
-- started work outlives it (narrowing a query to "not done" must not erase history).
CREATE TABLE IF NOT EXISTS work_item_session(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  provider      TEXT NOT NULL,
  item_key      TEXT NOT NULL,
  session_name  TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',
  branch        TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_item_session_membership ON work_item_session(membership_id)
