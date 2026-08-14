-- Postgres mirror of migrations/0039_tenant_login.sql (docs/61 §61.9.7).
ALTER TABLE tenant ADD COLUMN IF NOT EXISTS allowed_providers TEXT NOT NULL DEFAULT '';

ALTER TABLE tenant ADD COLUMN IF NOT EXISTS auto_join_domains TEXT NOT NULL DEFAULT '';

ALTER TABLE tenant ADD COLUMN IF NOT EXISTS allowed_domains TEXT NOT NULL DEFAULT ''
