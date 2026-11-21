-- The same column as migrations/0151_session_queue_category_id.sql. Forward compatible.
ALTER TABLE session ADD COLUMN IF NOT EXISTS queue_category_id TEXT NOT NULL DEFAULT '';
