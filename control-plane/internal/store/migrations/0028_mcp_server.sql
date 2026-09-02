-- Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031). A tenant_admin registers a
-- remote MCP server here and every member of that tenant receives the definition in
-- their Workspace, where the agent caches it and materializes it into each CLI config.
-- The definition lives in the CP DB because the CP is the only thing alive while a
-- member workspace is stopped, so it is the only place a distributed set can be held.
--
-- There is deliberately NO command or args or env column (ADR0031 decision 2). A
-- tenant-distributed stdio server would let an admin run an arbitrary command inside
-- every member container, which is root-equivalent power over the whole tenant. The
-- API refuses transport=stdio, and the absence of the columns is what stops that
-- refusal from being relaxed later by a one-line change.
--
-- headers_enc holds the header map as base64 AES-GCM ciphertext produced by the tenant
-- key custodian (key_ref names the tenant key, same envelope shape as the workspace
-- DEK). TEXT rather than BLOB so the column type is identical on SQLite and Postgres.
-- Deployments with no master key store the map as plaintext JSON with an empty key_ref,
-- matching how the Agent secret store degrades in dev.
--
-- targets is a comma list of assistant and session, kinds is a comma list of agent
-- kinds and empty means every kind. enabled and user_secret are 0/1 integers.
-- user_secret=1 distributes the URL and the header NAMES only, with each member filling
-- the values into their own encrypted store, so a tenant token is never written in
-- plaintext into every member container.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS mcp_server(
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  transport   TEXT NOT NULL DEFAULT 'http',
  url         TEXT NOT NULL DEFAULT '',
  headers_enc TEXT NOT NULL DEFAULT '',
  key_ref     TEXT NOT NULL DEFAULT '',
  targets     TEXT NOT NULL DEFAULT 'assistant,session',
  kinds       TEXT NOT NULL DEFAULT '',
  timeout_ms  INTEGER NOT NULL DEFAULT 0,
  enabled     INTEGER NOT NULL DEFAULT 1,
  user_secret INTEGER NOT NULL DEFAULT 0,
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_server_name ON mcp_server(tenant_id, name)
