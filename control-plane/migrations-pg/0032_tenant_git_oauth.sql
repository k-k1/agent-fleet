-- Postgres mirror of migrations/0048_tenant_git_oauth.sql (docs/71 + ADR0052).
-- Tenant-owned OAuth apps for the git providers. See the SQLite file for why the
-- row is the only source (env is not read) and why there is no approval status.
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
