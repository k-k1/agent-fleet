-- Provided model routes, phase Q0 (ADR 0129, issue #3578), Postgres mirror of
-- migrations/0126_provided_routes.sql. The data model only. Nothing in the request path reads
-- or writes these tables yet, so an upgrade changes no behaviour.
-- Every new table starts empty and every flag defaults to off, so the migration is forward
-- compatible. Amounts are integer micro-USD, the cloud_cost precedent, because admission
-- compares sums of them in a conditional write and a float sum would drift.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
--
-- provided_route_source: one registration of a credential source, tenant (tenant_id set) or
-- deployment (tenant_id empty). id is persistent. fingerprint covers the payer (role ARN,
-- external id version, Region set) and is what a session binds to. allow_list is a JSON array
-- of model ids with its own policy_version, bumped when the set changes. external_id is not a
-- secret (AWS third-party access). active is set only by the activation probes and is cleared
-- whenever the fingerprint changes.
CREATE TABLE IF NOT EXISTS provided_route_source(
  id                  TEXT PRIMARY KEY,
  source              TEXT NOT NULL CHECK (source IN ('tenant', 'deployment')),
  tenant_id           TEXT NOT NULL DEFAULT '',
  name                TEXT NOT NULL DEFAULT '',
  role_arn            TEXT NOT NULL,
  external_id         TEXT NOT NULL DEFAULT '',
  external_id_version INTEGER NOT NULL DEFAULT 0,
  regions             TEXT NOT NULL DEFAULT '[]',
  fingerprint         TEXT NOT NULL,
  allow_list          TEXT NOT NULL DEFAULT '[]',
  policy_version      INTEGER NOT NULL DEFAULT 1,
  active              INTEGER NOT NULL DEFAULT 0,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provided_route_source_tenant ON provided_route_source(tenant_id, source);
-- provided_route_grant: per tenant and source. granted is the super_admin's grant, a plain flag
-- whose zero is off (no row is off too). ceiling_micro is the tenant's monthly ceiling, NULL =
-- none (refused for a granted deployment source). members_only and member_default_micro are the
-- tenant_admin's: "Granted members only" and the budget of a member with no override, zero by
-- default so nobody may spend until a tenant_admin sets one.
CREATE TABLE IF NOT EXISTS provided_route_grant(
  tenant_id            TEXT NOT NULL,
  source               TEXT NOT NULL CHECK (source IN ('tenant', 'deployment')),
  granted              INTEGER NOT NULL DEFAULT 0,
  ceiling_micro        BIGINT,
  members_only         INTEGER NOT NULL DEFAULT 0,
  member_default_micro BIGINT NOT NULL DEFAULT 0,
  updated_at           TEXT NOT NULL,
  PRIMARY KEY(tenant_id, source)
);
-- provided_route_member: one member's grant (read only while members_only is on) and budget
-- override (NULL = the tenant default).
CREATE TABLE IF NOT EXISTS provided_route_member(
  membership_id TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  source        TEXT NOT NULL CHECK (source IN ('tenant', 'deployment')),
  granted       INTEGER NOT NULL DEFAULT 0,
  budget_micro  BIGINT,
  updated_at    TEXT NOT NULL,
  PRIMARY KEY(membership_id, source)
);
CREATE INDEX IF NOT EXISTS idx_provided_route_member_tenant ON provided_route_member(tenant_id);
-- provided_route_period: the spend of one month (UTC admission month, YYYY-MM) at one level.
-- membership_id empty is the tenant level. reserved_micro is the sum of the reservations still
-- in flight that were admitted in this month.
CREATE TABLE IF NOT EXISTS provided_route_period(
  tenant_id      TEXT NOT NULL,
  membership_id  TEXT NOT NULL,
  source         TEXT NOT NULL CHECK (source IN ('tenant', 'deployment')),
  month          TEXT NOT NULL,
  spent_micro    BIGINT NOT NULL DEFAULT 0,
  reserved_micro BIGINT NOT NULL DEFAULT 0,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY(tenant_id, membership_id, source, month)
);
-- provided_route_call: one admitted call's reservation. binding is the bound source,
-- "<source id>#<fingerprint>". month and price_version are frozen at admission.
-- final_micro is NULL until the row is settled, exactly once. The CHECKs repeat the Go
-- constants (providedroutes.Source*, State*) so a stray value is refused by the database too.
CREATE TABLE IF NOT EXISTS provided_route_call(
  call_id        TEXT PRIMARY KEY,
  membership_id  TEXT NOT NULL,
  tenant_id      TEXT NOT NULL,
  source         TEXT NOT NULL CHECK (source IN ('tenant', 'deployment')),
  binding        TEXT NOT NULL,
  model          TEXT NOT NULL,
  api            TEXT NOT NULL,
  month          TEXT NOT NULL,
  price_version  TEXT NOT NULL,
  reserved_micro BIGINT NOT NULL,
  final_micro    BIGINT,
  state          TEXT NOT NULL CHECK (state IN ('in_flight', 'settled')),
  admitted_at    TEXT NOT NULL,
  settled_at     TEXT NOT NULL DEFAULT '',
  cp_instance    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provided_route_call_state ON provided_route_call(state, cp_instance);
CREATE INDEX IF NOT EXISTS idx_provided_route_call_member ON provided_route_call(membership_id, source, state)
