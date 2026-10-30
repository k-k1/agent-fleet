-- Tenant-distributed MCP servers that require OAuth (ADR 0119 decision 9). auth is '' (static
-- headers or none, every row written before this column) or 'oauth'. An oauth row carries no
-- header and no user_secret: each member signs in for themselves from the Console, and the
-- grant stays in that member's workspace, never in this table.
ALTER TABLE mcp_server ADD COLUMN auth TEXT NOT NULL DEFAULT ''
