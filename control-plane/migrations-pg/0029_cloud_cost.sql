-- Cloud cost, the AWS invoice attributed by cost allocation tag (docs/log/67, ADR 0048).
--
-- NOT the same thing as usage_daily next door. That one counts workspace occupancy in
-- SECONDS and exists on every runtime — this one holds REAL MONEY from Cost Explorer and
-- exists only where there is an AWS bill. They are deliberately separate tables because
-- they are separate claims: an estimate derived from seconds would go stale silently the
-- moment instance prices or slot types changed (ADR 0048 決定 2).
--
-- membership_id = '' is the SHARED bucket: NAT, Route53, ALB, RDS, EFS, the CP's own
-- Fargate, tax, and the warm pool slots nobody is holding. Measured on the reference
-- deployment that is ~78% of the bill, and it is NOT divided among people — dividing it
-- would turn the invoice back into an estimate (決定 4).
--
-- Amounts are integer MICRO-units of the billing currency (1 USD = 1_000_000), never
-- floats: a day of per-member rows summed in float64 drifts, and money that does not add
-- up is worse than no money at all.
CREATE TABLE cloud_cost_daily (
  day            TEXT NOT NULL,               -- YYYY-MM-DD (UTC), as Cost Explorer reports it
  membership_id  TEXT NOT NULL,               -- '' = shared / unattributable
  tenant_id      TEXT NOT NULL,               -- '' when unknown or shared
  service        TEXT NOT NULL,               -- AWS service name as CE returns it
  unblended      BIGINT NOT NULL DEFAULT 0,  -- micro-units — what is invoiced
  amortized      BIGINT NOT NULL DEFAULT 0,  -- micro-units — differs only with RI/Savings Plans
  currency       TEXT NOT NULL DEFAULT '',    -- as AWS returned it — never converted
  -- Cost Explorer marks recent days estimated and they DO change. Kept so the UI can say
  -- "not final yet" instead of showing a number that quietly moves.
  estimated      INTEGER NOT NULL DEFAULT 0,
  updated_at     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, membership_id, service)
);
CREATE INDEX idx_cloud_cost_tenant_day ON cloud_cost_daily(tenant_id, day);
CREATE INDEX idx_cloud_cost_membership_day ON cloud_cost_daily(membership_id, day);
