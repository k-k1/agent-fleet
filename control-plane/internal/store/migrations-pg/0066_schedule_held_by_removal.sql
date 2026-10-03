-- Member removal (issue #1087), Postgres mirror of
-- migrations/0081_schedule_held_by_removal.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS held_by_removal INTEGER NOT NULL DEFAULT 0
