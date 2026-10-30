-- Settings AWS profiles that share one IAM Identity Center sign-in (ADR 0125, issue #3014).
--
-- A sign-in is a member-named portal (start URL and SSO region) that SSO profiles opt into.
-- The workspace renders one [sso-session af-<name>] for it, so one login serves every member.
-- ssm_profile.sign_in_id is '' for a profile with a sign-in of its own (every row written
-- before this migration): nothing about it changes. A member profile keeps a copy of the
-- sign-in's start_url and sso_region, which the store rewrites with the sign-in, so a reader
-- that does not know sign-ins still sees a whole profile. Nothing here is a secret.
-- Forward compatible: an older binary ignores the table and the column.
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
