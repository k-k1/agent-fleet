-- A counter shared by every Control Plane task (issue #1601), Postgres mirror of
-- migrations/0087_cp_counter.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS cp_counter (
    name  TEXT PRIMARY KEY,
    value BIGINT NOT NULL
)
