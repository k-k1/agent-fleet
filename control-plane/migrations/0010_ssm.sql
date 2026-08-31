-- SSM login (docs/log/p3-ssm-session.md): per-member SSO sessions and SSM host
-- bookmarks, personal scope (one row = one membership = identity × tenant). NO AWS
-- secrets are stored here: only non-secret SSO config (start URL / account / role)
-- and host coordinates (instance / run-as document / region). Short-lived AWS
-- credentials are obtained by the in-container aws CLI via `aws sso login` at session
-- start and cached in the workspace home — they never reach the Control Plane.
CREATE TABLE IF NOT EXISTS sso_session(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  start_url     TEXT NOT NULL,
  sso_region    TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sso_session_membership ON sso_session(membership_id);

CREATE TABLE IF NOT EXISTS ssm_host(
  id             TEXT PRIMARY KEY,
  membership_id  TEXT NOT NULL,
  alias          TEXT NOT NULL,
  sso_session_id TEXT NOT NULL DEFAULT '',
  account_id     TEXT NOT NULL DEFAULT '',
  role_name      TEXT NOT NULL DEFAULT '',
  region         TEXT NOT NULL DEFAULT '',
  instance_id    TEXT NOT NULL,
  document_name  TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ssm_host_membership ON ssm_host(membership_id);
