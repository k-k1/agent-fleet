-- A lease that makes one loop single-runner across Control Plane tasks (issue #1603). Two CPs
-- overlap during a rolling replacement, and a loop that drives AWS one step per tick (the
-- golden auto-bake) must not be driven by both. name is the loop, holder the process that runs
-- it, expires_at (RFC 3339 UTC, fixed width so text order is time order) when another may take
-- over. The holder renews it while it runs, and deletes it when it stops.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS cp_lease (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
)
