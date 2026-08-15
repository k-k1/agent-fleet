-- Postgres mirror of migrations/0042_tenant_hidden_providers.sql (docs/61 §61.15.9).
ALTER TABLE tenant ADD COLUMN IF NOT EXISTS hidden_providers TEXT NOT NULL DEFAULT ''
