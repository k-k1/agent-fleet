-- Postgres mirror of migrations/0035_shared_session_catalog_parent_wc.sql.
ALTER TABLE shared_session_catalog ADD COLUMN parent_working_copy_id TEXT NOT NULL DEFAULT '';
