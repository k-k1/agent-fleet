-- Scheduled execution (docs/log/38 + ADR0021), Postgres mirror of migrations/0022_schedule.sql.
-- See that file for the column semantics. DDL is dialect-neutral here (TEXT/INTEGER),
-- so the two stay identical apart from living in the pg migration series.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS schedule(
  id             TEXT PRIMARY KEY,
  membership_id  TEXT NOT NULL,
  tenant_id      TEXT NOT NULL,
  owner_conv     TEXT NOT NULL DEFAULT '',
  spec_kind      TEXT NOT NULL,
  spec           TEXT NOT NULL,
  spec_label     TEXT NOT NULL DEFAULT '',
  tz             TEXT NOT NULL DEFAULT 'UTC',
  wake_policy    TEXT NOT NULL DEFAULT 'wake',
  session_mode   TEXT NOT NULL DEFAULT 'new',
  reuse_target   TEXT NOT NULL DEFAULT '',
  agent_kind     TEXT NOT NULL DEFAULT 'claude',
  model          TEXT NOT NULL DEFAULT '',
  repo           TEXT NOT NULL DEFAULT '',
  worktree       TEXT NOT NULL DEFAULT '',
  new_branch     INTEGER NOT NULL DEFAULT 0,
  prompt         TEXT NOT NULL DEFAULT '',
  overlap_policy TEXT NOT NULL DEFAULT 'skip',
  enabled        INTEGER NOT NULL DEFAULT 1,
  next_run       TEXT NOT NULL DEFAULT '',
  last_run       TEXT NOT NULL DEFAULT '',
  last_status    TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_due ON schedule(enabled, next_run);
CREATE INDEX IF NOT EXISTS idx_schedule_membership ON schedule(membership_id)
