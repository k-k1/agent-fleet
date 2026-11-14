-- Credential policy, ADR 0138 phase M1 (issue 4168). Mirror: migrations-pg/0118_route_policy.sql.
-- route_policy holds the layers apart, one row each, so that writing or clearing one never
-- touches another (D8): scope 'ceiling' (tenant_id empty) is the operator ceiling as the last
-- Control Plane to start read it from its environment, recorded so a change of it takes a
-- revision too, 'deployment' (tenant_id empty) the super_admin's deployment-wide rule,
-- 'tenant_cap' the super_admin's cap for one tenant, 'tenant' the tenant_admin's own rule. rule is the JSON of routepolicy.Rule, '{}' meaning no restriction,
-- which is also what a missing row reads as, so the migration changes no behaviour. revision is
-- taken from route_policy_seq in the write's transaction, so every write is strictly later than
-- every earlier one and a member's effective revision only ever grows.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS route_policy(
  scope      TEXT NOT NULL CHECK (scope IN ('ceiling', 'deployment', 'tenant_cap', 'tenant')),
  tenant_id  TEXT NOT NULL DEFAULT '',
  rule       TEXT NOT NULL DEFAULT '{}',
  revision   BIGINT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(scope, tenant_id)
);
CREATE TABLE IF NOT EXISTS route_policy_seq(
  id INTEGER PRIMARY KEY CHECK (id = 1),
  n  BIGINT NOT NULL
);
INSERT INTO route_policy_seq(id, n) VALUES(1, 0) ON CONFLICT(id) DO NOTHING;
