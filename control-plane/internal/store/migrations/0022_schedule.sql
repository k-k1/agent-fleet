-- Scheduled execution (docs/log/38 + ADR0021): operator-authored cron/interval/once
-- tasks that fire on a wall-clock and drive a fleet session. The definition lives
-- in the CP DB because the CP is the only thing alive while a workspace is stopped
-- (the in-container agent is gone), so only the CP can watch the clock and wake it.
-- spec_kind is 'cron' (spec is a 5-field cron expr), 'interval' (spec is whole
-- seconds) or 'once' (spec is an RFC3339 absolute instant). spec is the evaluated
-- source of truth, tz is the IANA zone the cron is evaluated in (DST included), and
-- spec_label keeps the operator's original natural-language phrasing for display.
-- next_run/last_run/last_status are the fire ledger: next_run is the next due UTC
-- RFC3339 instant (empty means never/disabled), so the due query is enabled=1 with a
-- non-empty next_run at or before now. new_branch and enabled are 0/1 integers.
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
