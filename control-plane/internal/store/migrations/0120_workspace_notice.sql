-- Which admin notices (#2427) were already raised for a workspace: one row per (workspace, kind)
-- while the condition holds, deleted when it clears. The row IS the "already notified" mark, so
-- a Control Plane restart or a second replica cannot raise the same crossing again, and the
-- primary key makes the claim a single conditional insert.
-- Forward compatible: a new table that nothing existing reads. No backfill: a condition that
-- already holds raises once at the next sample.
CREATE TABLE IF NOT EXISTS workspace_notice(
  workspace_id TEXT NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,
  raised_at    TEXT NOT NULL,
  PRIMARY KEY (workspace_id, kind)
)
