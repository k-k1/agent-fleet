-- OAuth authorization server for the /mcp endpoint (ADR 0118): dynamically registered clients,
-- one grant per connection, and the short-lived consents, codes, access tokens and refresh
-- tokens that hang off it. Every secret value is stored as a SHA-256 hash only, so no column
-- here is sealed and `rewrap-keys` is untouched.
--
-- No foreign keys: membershipCascade lists what it removes (the same as dev_idp_grant), and
-- rows carry their own expiry and are purged on the next insert.

-- Only DCR clients live here, a CIMD client is the document at its client_id and is cached in
-- memory. redirect_uris is a JSON array.
CREATE TABLE IF NOT EXISTS mcp_oauth_client(
  client_id     TEXT PRIMARY KEY,
  name          TEXT NOT NULL DEFAULT '',
  redirect_uris TEXT NOT NULL,
  created_at    BIGINT NOT NULL
);

-- A grant is what the member sees and revokes. code_hash names the authorization code it was
-- minted from, so replaying that code can take the grant down.
CREATE TABLE IF NOT EXISTS mcp_oauth_grant(
  id               TEXT PRIMARY KEY,
  client_id        TEXT NOT NULL,
  client_name      TEXT NOT NULL DEFAULT '',
  redirect_host    TEXT NOT NULL DEFAULT '',
  identity_id      TEXT NOT NULL,
  membership_id    TEXT NOT NULL,
  tenant_id        TEXT NOT NULL DEFAULT '',
  scope            TEXT NOT NULL,
  resource         TEXT NOT NULL,
  code_hash        TEXT NOT NULL DEFAULT '',
  created_at       BIGINT NOT NULL,
  last_used_at     BIGINT NOT NULL DEFAULT 0,
  idle_expires_at  BIGINT NOT NULL,
  abs_expires_at   BIGINT NOT NULL,
  revoked_at       BIGINT NOT NULL DEFAULT 0,
  revoked_reason   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS mcp_oauth_grant_identity ON mcp_oauth_grant(identity_id);
CREATE INDEX IF NOT EXISTS mcp_oauth_grant_client ON mcp_oauth_grant(client_id);
CREATE INDEX IF NOT EXISTS mcp_oauth_grant_membership ON mcp_oauth_grant(membership_id);
CREATE INDEX IF NOT EXISTS mcp_oauth_grant_code ON mcp_oauth_grant(code_hash);

-- kind is consent | code | access | refresh. A spent consent / code / refresh row stays until it
-- expires (used_at != 0) so that presenting it again is recognised as a replay.
CREATE TABLE IF NOT EXISTS mcp_oauth_token(
  hash          TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  grant_id      TEXT NOT NULL DEFAULT '',
  client_id     TEXT NOT NULL,
  client_name   TEXT NOT NULL DEFAULT '',
  identity_id   TEXT NOT NULL,
  membership_id TEXT NOT NULL DEFAULT '',
  redirect_uri  TEXT NOT NULL DEFAULT '',
  challenge     TEXT NOT NULL DEFAULT '',
  scope         TEXT NOT NULL DEFAULT '',
  resource      TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT '',
  expires_at    BIGINT NOT NULL,
  used_at       BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS mcp_oauth_token_grant ON mcp_oauth_token(grant_id);
CREATE INDEX IF NOT EXISTS mcp_oauth_token_membership ON mcp_oauth_token(membership_id);
CREATE INDEX IF NOT EXISTS mcp_oauth_token_expires ON mcp_oauth_token(expires_at);
