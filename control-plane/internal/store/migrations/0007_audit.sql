-- Audit log (docs/decisions/0006, P3-6 MCP admin tools).
-- Records administrative actions with the acting principal. actor_kind is one of
-- user / admin / mcp / system, and actor_id is the identity id or PAT id behind it.
-- P3-6 admin MCP write tools (stop_workspace / stop_session / set_user_quota)
-- write here, and read tooling (tail_audit / admin UI) follows. tenant_id scopes
-- the entry to the tenant the action touched ('' for deployment-wide).
CREATE TABLE audit_log (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL DEFAULT '',
  actor_kind TEXT NOT NULL,
  actor_id   TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '',
  at         TEXT NOT NULL
);
CREATE INDEX idx_audit_tenant_at ON audit_log(tenant_id, at);
