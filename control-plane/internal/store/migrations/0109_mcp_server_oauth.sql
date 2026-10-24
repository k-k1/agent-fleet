-- Tenant-distributed MCP servers that require OAuth (ADR 0119 decision 9). auth is '' (static
-- headers or none, every row written before this column) or 'oauth'. An oauth row carries no
-- header and no user_secret: each member signs in for themselves from the Console, and the
-- grant stays in that member's workspace, never in this table.
--
-- oauth_issuer and oauth_client_id name a client the tenant registered at the server's
-- authorization server (empty = none). oauth_secret_enc is that client's secret, sealed like
-- headers_enc under oauth_key_ref, and empty for a public client. The secret is never
-- distributed: with one, the CP runs the members' token requests itself.
ALTER TABLE mcp_server ADD COLUMN auth TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_secret_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_server ADD COLUMN oauth_key_ref TEXT NOT NULL DEFAULT ''
