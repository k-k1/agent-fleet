-- The scope list a tenant-distributed OAuth MCP server's sign-in requests (ADR 0146 decision 2),
-- as space-separated scope tokens. Empty = none of its own: the Agent then asks for what the
-- server's challenge or metadata names. Additive.
ALTER TABLE mcp_server ADD COLUMN oauth_scopes TEXT NOT NULL DEFAULT ''
