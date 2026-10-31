-- Postgres mirror of migrations/0117_ssm_sign_in.sql (ADR 0125, issue #3014).
-- See the SQLite file for what the table and the column mean.
CREATE TABLE IF NOT EXISTS ssm_sign_in(
  id TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  name TEXT NOT NULL,
  start_url TEXT NOT NULL,
  sso_region TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssm_sign_in_member_name ON ssm_sign_in(membership_id, name);
ALTER TABLE ssm_profile ADD COLUMN sign_in_id TEXT NOT NULL DEFAULT ''
