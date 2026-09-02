-- Postgres mirror of migrations/0043_identity_realm_subject.sql (docs/log/61 §61.15.10).
ALTER TABLE identity_provider ADD COLUMN IF NOT EXISTS realm_claim TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_provider ADD COLUMN IF NOT EXISTS realm_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_idp ADD COLUMN IF NOT EXISTS link_claim TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_identity_provider_realm_claim
    ON identity_provider(realm, realm_claim, realm_subject)
