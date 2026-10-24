-- Tenant-registered OAuth clients for MCP servers (ADR 0119, P5), Postgres mirror of
-- migrations/0110_mcp_server_oauth_client.sql. See that file for the column semantics.
ALTER TABLE mcp_server ADD COLUMN oauth_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_secret_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_key_ref TEXT NOT NULL DEFAULT ''
