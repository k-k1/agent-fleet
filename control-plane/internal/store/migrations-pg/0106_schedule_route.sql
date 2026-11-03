-- ADR 0128 P1a, Postgres mirror of migrations/0121_schedule_route.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS route TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS route_connection TEXT NOT NULL DEFAULT ''
