-- Which CLIs failed to install on the workspace's last start, as the usage sampler last saw
-- it. Read by the tenant member roster and the member detail, which draw from the DB alone
-- while the workspace is stopped. failed is a JSON object of tool to reason, or an empty
-- string when the start reported no failure. observed_at orders writes the way
-- workspace_disk.observed_at does, so a slow sweep cannot roll a newer outcome back.
-- Forward compatible: a new table that nothing existing reads. No backfill, a row appears at
-- the next sample.
CREATE TABLE IF NOT EXISTS workspace_boot_install(
  workspace_id TEXT PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
  failed       TEXT NOT NULL DEFAULT '',
  observed_at  TEXT NOT NULL
)
