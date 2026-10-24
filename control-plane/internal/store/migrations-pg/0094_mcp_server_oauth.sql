-- Tenant-distributed MCP servers that require OAuth (ADR 0119 decision 9), Postgres mirror of
-- migrations/0109_mcp_server_auth.sql. See that file for the column semantics.
ALTER TABLE mcp_server ADD COLUMN auth TEXT NOT NULL DEFAULT ''
