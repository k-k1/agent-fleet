-- Postgres mirror of migrations/0169_session_place.sql. Additive: a new nullable column.
ALTER TABLE session ADD COLUMN IF NOT EXISTS place TEXT;
