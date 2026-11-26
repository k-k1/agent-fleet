-- Postgres mirror of migrations/0171_session_stopped_at.sql. Additive: a new column with a default.
ALTER TABLE session ADD COLUMN IF NOT EXISTS stopped_at TEXT NOT NULL DEFAULT '';
