-- The same columns as migrations/0148_session_origin.sql. Forward compatible.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE session ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';
ALTER TABLE session ADD COLUMN IF NOT EXISTS origin_session TEXT NOT NULL DEFAULT '';
