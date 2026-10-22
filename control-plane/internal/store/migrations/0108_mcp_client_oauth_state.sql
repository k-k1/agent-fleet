-- Sign-in state for MCP servers that require OAuth (ADR 0119 decision 4): af is the OAuth
-- client here, the opposite side from 0106. One row per sign-in in flight. state_hash is the
-- SHA-256 of the `state` sent to the authorization server, so a database reader cannot finish
-- somebody's sign-in. The row is deleted when the callback consumes it (success or not) and
-- carries its own expiry, and rows past it are purged on the next insert.
--
-- No foreign keys: membershipCascade removes what a membership leaves behind.
CREATE TABLE IF NOT EXISTS mcp_client_oauth_state(
  state_hash    TEXT PRIMARY KEY,
  identity_id   TEXT NOT NULL,
  membership_id TEXT NOT NULL,
  tenant_id     TEXT NOT NULL DEFAULT '',
  workspace_id  TEXT NOT NULL DEFAULT '',
  server_id     TEXT NOT NULL,
  server_name   TEXT NOT NULL DEFAULT '',
  issuer        TEXT NOT NULL DEFAULT '',
  registration  TEXT NOT NULL DEFAULT '',
  created_at    BIGINT NOT NULL,
  expires_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS mcp_client_oauth_state_expires ON mcp_client_oauth_state(expires_at);
CREATE INDEX IF NOT EXISTS mcp_client_oauth_state_membership ON mcp_client_oauth_state(membership_id);
