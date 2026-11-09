-- Postgres mirror of migrations/0129_tenant_watch_sources.sql.
-- See the SQLite file for what the column means.
ALTER TABLE tenant ADD COLUMN watch_sources INTEGER NOT NULL DEFAULT 0
