-- Personal Access Tokens (docs/decisions/0006, P3-6 MCP).
-- A user issues PATs in the Console to authenticate the MCP endpoint. The token
-- references identity+membership, role is resolved live at call time (never
-- frozen here), scope is chosen at issuance and clamped to the issuer's ceiling.
-- token_hash = SHA-256 hex of the presented secret. The plaintext is never stored.
CREATE TABLE pat (
  id            TEXT PRIMARY KEY,
  identity_id   TEXT NOT NULL REFERENCES identity(id),
  membership_id TEXT REFERENCES membership(id),
  token_hash    TEXT NOT NULL UNIQUE,
  scope         TEXT NOT NULL DEFAULT 'read',
  name          TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  expires_at    TEXT,
  revoked_at    TEXT,
  last_used_at  TEXT
);
CREATE INDEX idx_pat_identity ON pat(identity_id);
