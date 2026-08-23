-- Tenant-owned OAuth apps for the git providers (docs/71 + ADR0052). The Console's
-- "connect with OAuth" buttons for GitHub and Bitbucket used to run on a DEPLOYMENT
-- wide OAuth app named by env (GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY+SECRET).
-- That put the operator in the loop for something that is a tenant's own account
-- with its own git hosting organisation, and it could not differ per tenant at all.
--
-- The row is the ONLY source now. env is not read, not even as a fallback, so there
-- is one place to look when a button is missing (決定 2).
--
-- provider is the slug the rest of CP already uses for the connection: github or
-- bitbucket. One row per (tenant, provider) - a tenant registers one app per host.
--
-- client_id is the OAuth app's public identifier. GitHub's device flow needs only
-- that, so its rows carry an empty secret on purpose - it is not an unfinished row.
-- Bitbucket's authorization code grant needs both.
--
-- secret_enc holds client_secret as base64 AES-GCM ciphertext from the tenant key
-- custodian and key_ref names the tenant key, the same envelope tenant_idp.secret_enc
-- and mcp_server.headers_enc use. A deployment with no master key stores plaintext
-- with an empty key_ref, the way the rest of CP degrades in dev.
--
-- There is no status column, deliberately. A tenant_idp row needs a super_admin to
-- approve it because registering an IdP declares who somebody IS. An OAuth app for
-- cloning repositories declares nothing about identity, the redirect_uri is the CP's
-- own and the token only ever reaches the member's workspace - so the tenant_admin
-- owns it end to end (ADR0052 決定 3).
CREATE TABLE IF NOT EXISTS tenant_git_oauth(
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  provider   TEXT NOT NULL,
  client_id  TEXT NOT NULL,
  secret_enc TEXT NOT NULL DEFAULT '',
  key_ref    TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_git_oauth ON tenant_git_oauth(tenant_id, provider)
