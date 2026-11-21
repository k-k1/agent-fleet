-- The same column as migrations/0149_session_schedule_id.sql. Forward compatible.
ALTER TABLE session ADD COLUMN IF NOT EXISTS schedule_id TEXT NOT NULL DEFAULT '';
