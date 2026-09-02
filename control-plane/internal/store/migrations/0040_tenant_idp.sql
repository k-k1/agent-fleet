-- Tenant-defined login providers (docs/log/61 §61.11 + ADR0043 決定 29-33). A group of
-- companies has one Entra tenant per subsidiary, so the IdP definition itself has to
-- be per Agent Fleet tenant rather than per deployment. Putting it in env would mean
-- editing a file on the host and restarting CP every time a subsidiary is onboarded,
-- which contradicts "creating a tenant needs no restart" (docs/log/61 §61.10.3).
--
-- ★ status is the whole point of this table, not a detail. A tenant_admin writes the
-- row, a super_admin ACTIVATES it. Registering an IdP is the power to declare who
-- someone is, and user_key plus the deployment role are keyed by email across the
-- WHOLE deployment, so an admin who could enable their own IdP could mint a token
-- claiming the operator's address and take the deployment (docs/log/61 §61.11.2). Rows
-- are therefore born pending, only active rows produce a login button or a session,
-- and changing issuer or client_id or trust sends the row back to pending because
-- the approval was given to that issuer and not to the row.
--
-- suspended is reachable by the tenant_admin too - stopping is always allowed to be
-- faster than starting.
--
-- name is the provider id INSIDE the tenant. The id the rest of CP sees is
-- t:<tenant-slug>:<name> and never collides with an env-defined provider, so a
-- tenant cannot create a row called google and shadow the deployment's Google.
--
-- secret_enc holds client_secret as base64 AES-GCM ciphertext from the tenant key
-- custodian, key_ref names the tenant key - the same envelope mcp_server.headers_enc
-- uses. A deployment with no master key stores plaintext with an empty key_ref, the
-- way the rest of CP degrades in dev.
--
-- allowed_tids and allowed_domains are CSV and scoped to this row. They are NOT
-- backed by the deployment-wide allowlist - a tenant IdP must not be able to widen
-- the deployment's entry gate (docs/log/61 §61.11.3-3).
--
-- Forward compatible: an older CP binary never selects this table, so rolling the
-- binary back leaves a deployment that simply has no tenant-defined providers.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS tenant_idp(
  id              TEXT PRIMARY KEY,
  tenant_id       TEXT NOT NULL,
  name            TEXT NOT NULL,
  label_ja        TEXT NOT NULL DEFAULT '',
  label_en        TEXT NOT NULL DEFAULT '',
  issuer          TEXT NOT NULL,
  client_id       TEXT NOT NULL,
  secret_enc      TEXT NOT NULL DEFAULT '',
  key_ref         TEXT NOT NULL DEFAULT '',
  trust           TEXT NOT NULL,
  allowed_tids    TEXT NOT NULL DEFAULT '',
  allowed_domains TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'pending',
  approved_by     TEXT NOT NULL DEFAULT '',
  approved_at     TEXT NOT NULL DEFAULT '',
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_idp_name ON tenant_idp(tenant_id, name)
