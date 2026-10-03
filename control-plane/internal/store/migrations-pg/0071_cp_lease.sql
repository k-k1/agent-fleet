-- A lease that makes one loop single-runner across Control Plane tasks (issue #1603), Postgres
-- mirror of migrations/0086_cp_lease.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS cp_lease (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
)
