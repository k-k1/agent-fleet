-- A lease that makes one loop single-runner across Control Plane tasks (issue #1603). Two CPs
-- overlap during a rolling replacement, and a loop that drives AWS one step per tick (the
-- golden auto-bake) must not be driven by both. name is the loop, holder the process that runs
-- it, expires_ms (Unix milliseconds by the clock of the database, never of a CP) when another may
-- take over. The holder renews it while it runs, and a holder that stops renewing lets it expire.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS cp_lease (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_ms INTEGER NOT NULL
)
