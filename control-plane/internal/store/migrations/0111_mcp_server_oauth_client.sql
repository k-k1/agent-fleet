-- A client the tenant registered at a tenant-distributed OAuth MCP server's authorization server
-- (ADR 0119, P5). oauth_issuer and oauth_client_id name it (empty = none). oauth_secret_enc is
-- that client's secret, sealed like headers_enc under oauth_key_ref, and empty for a public
-- client. The secret is never distributed: with one, the CP runs the members' token requests
-- itself.
ALTER TABLE mcp_server ADD COLUMN oauth_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_secret_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_key_ref TEXT NOT NULL DEFAULT ''
