-- Definition scopes for tenant-distributed OAuth MCP servers (ADR 0146 decision 2), Postgres
-- mirror of migrations/0171_mcp_server_oauth_scopes.sql. See that file for the column semantics.
ALTER TABLE mcp_server ADD COLUMN oauth_scopes TEXT NOT NULL DEFAULT ''
