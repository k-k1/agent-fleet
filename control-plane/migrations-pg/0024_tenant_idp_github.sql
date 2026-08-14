-- Postgres mirror of migrations/0041_tenant_idp_github.sql (docs/61 §61.15).
ALTER TABLE tenant_idp ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'oidc';
ALTER TABLE tenant_idp ADD COLUMN IF NOT EXISTS allowed_orgs TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_provider ADD COLUMN IF NOT EXISTS realm TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_identity_provider_realm ON identity_provider(realm, subject)
