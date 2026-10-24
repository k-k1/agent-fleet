-- Tenant-distributed MCP servers that require OAuth (ADR 0119 decision 9), Postgres mirror of
-- migrations/0109_mcp_server_oauth.sql. See that file for the column semantics.
ALTER TABLE mcp_server ADD COLUMN auth TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_secret_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_key_ref TEXT NOT NULL DEFAULT ''
