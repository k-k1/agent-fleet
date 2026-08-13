-- Postgres mirror of migrations/0037_shared_session_catalog_activity.sql.
ALTER TABLE shared_session_catalog ADD COLUMN activity TEXT NOT NULL DEFAULT '';
