-- Per-member access to the self-hosted engine roles (#1215). A tenant_admin can restrict
-- a role ("llm" or "image") to members they have granted it to. This sits UNDER the
-- super_admin gate in the tenant limits blob (allow_engine_llm / allow_engine_image, ADR
-- 0084 decision 7), never beside it: a tenant denied a role stays denied whatever is here.
--
-- Its own tables rather than the limits blob because that blob is rewritten whole by the
-- super_admin save, and this is the tenant_admin's setting.
--
-- engine_access_policy: a row means "this role is for granted members only". No row is
-- the default, every member, so a deployment upgrading to this changes nothing.
CREATE TABLE IF NOT EXISTS engine_access_policy(
  tenant_id  TEXT NOT NULL,
  role       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(tenant_id, role)
);
-- engine_access_grant: one row per (member, role) granted. Only read while the role is
-- members-only, and kept when the mode is switched back so re-enabling it restores the list.
CREATE TABLE IF NOT EXISTS engine_access_grant(
  membership_id TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  role          TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  PRIMARY KEY(membership_id, role)
);
CREATE INDEX IF NOT EXISTS idx_engine_access_grant_tenant ON engine_access_grant(tenant_id)
