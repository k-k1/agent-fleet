-- A random credential-store key per member's home (issue #1646, ADR 0005 addendum 2026-10-10).
--
-- Keyed by membership, not workspace: a home can outlive its workspace row (DeleteWorkspace
-- drops wrapped_dek), and a random key that went with the row would make a kept home's
-- secrets.enc unreadable. The row is removed only when the home itself is destroyed.
--
-- ciphertext is the key sealed by the key custodian under key_ref (the tenant id), the
-- envelope wrapped_dek uses. scheme is 'migrating' while the CP still injects the derived key
-- beside it (AF_SECRET_KEY + AF_SECRET_KEY_NEXT), and 'random' once the home's Agent has
-- reported its store sealed under this key.
CREATE TABLE IF NOT EXISTS home_dek(
  membership_id TEXT PRIMARY KEY,
  ciphertext    TEXT NOT NULL,
  key_ref       TEXT NOT NULL,
  scheme        TEXT NOT NULL DEFAULT 'migrating' CHECK (scheme IN ('migrating','random')),
  created_at    TEXT NOT NULL,
  migrated_at   TEXT NOT NULL DEFAULT ''
)
