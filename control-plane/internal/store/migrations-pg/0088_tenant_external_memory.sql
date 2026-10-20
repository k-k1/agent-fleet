-- Postgres mirror of migrations/0103_tenant_external_memory.sql.
-- See the SQLite file for what the column means.
ALTER TABLE tenant ADD COLUMN external_memory INTEGER NOT NULL DEFAULT 1
