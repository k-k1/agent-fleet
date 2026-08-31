-- Refactor SSM login config into two clear tiers (docs/log/p3-ssm-session.md).
-- ssm_profile is the COMMON auth bundle (SSO portal plus account/role/default region)
-- reused by many hosts, mapping to one ~/.aws named profile. ssm_host keeps only the
-- PER-INSTANCE bits (alias, instance id, run-as document, optional region override)
-- plus the profile it authenticates with. This replaces the flat sso_session plus fat
-- ssm_host from 0010 (no real data yet). The old ssm_host columns account_id, role_name
-- and sso_session_id stay in place but unused, and new code reads profile_id instead.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS ssm_profile(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  label         TEXT NOT NULL,
  start_url     TEXT NOT NULL DEFAULT '',
  sso_region    TEXT NOT NULL DEFAULT '',
  account_id    TEXT NOT NULL DEFAULT '',
  role_name     TEXT NOT NULL DEFAULT '',
  region        TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ssm_profile_membership ON ssm_profile(membership_id);

ALTER TABLE ssm_host ADD COLUMN profile_id TEXT NOT NULL DEFAULT '';

DROP TABLE IF EXISTS sso_session;
