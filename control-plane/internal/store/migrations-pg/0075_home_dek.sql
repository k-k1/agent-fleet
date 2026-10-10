-- Postgres mirror of migrations/0090_home_dek.sql (issue #1646).
-- See the SQLite file for what each column means.
CREATE TABLE IF NOT EXISTS home_dek(
  membership_id TEXT PRIMARY KEY,
  ciphertext    TEXT NOT NULL,
  key_ref       TEXT NOT NULL,
  scheme        TEXT NOT NULL DEFAULT 'migrating' CHECK (scheme IN ('migrating','random')),
  created_at    TEXT NOT NULL,
  migrated_at   TEXT NOT NULL DEFAULT ''
)
