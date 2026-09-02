-- Postgres mirror of migrations/0034_shared_session_catalog_worktree.sql.
ALTER TABLE shared_session_catalog ADD COLUMN worktree INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shared_session_catalog ADD COLUMN parent TEXT NOT NULL DEFAULT '';
