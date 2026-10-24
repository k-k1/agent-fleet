-- Existing tenants keep MCP OAuth enabled. Disabling only affects OAuth grants.
ALTER TABLE tenant ADD COLUMN mcp_oauth_disabled INTEGER NOT NULL DEFAULT 0;
