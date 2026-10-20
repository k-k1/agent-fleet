-- The last home-disk reading per workspace, written by the usage sampler and read by the
-- tenant member roster (#2388). total is 0 when the reading has no denominator (no disk quota
-- set on a local or native deployment). observed_at is when the reading was taken, so a
-- stopped workspace's card can say how old it is.
-- Forward compatible: a new table, nothing existing reads it, and an older Control Plane that
-- starts against this schema ignores it. No backfill, a row appears at the next sample.
CREATE TABLE IF NOT EXISTS workspace_disk(
  workspace_id TEXT PRIMARY KEY,
  used         BIGINT NOT NULL,
  total        BIGINT NOT NULL DEFAULT 0,
  observed_at  TEXT NOT NULL
)
