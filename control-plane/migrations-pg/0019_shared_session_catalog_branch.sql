-- Postgres mirror of migrations/0036_shared_session_catalog_branch.sql.
ALTER TABLE shared_session_catalog ADD COLUMN branch TEXT NOT NULL DEFAULT '';
