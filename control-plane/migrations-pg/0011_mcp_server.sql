-- Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031), Postgres mirror of
-- migrations/0028_mcp_server.sql. See that file for the column semantics and for why
-- there is no command or args or env column. The DDL is dialect-neutral
-- (TEXT/INTEGER), so the two files stay identical apart from the migration series.
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
