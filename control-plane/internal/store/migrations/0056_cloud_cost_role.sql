-- The shared bucket, split by what the money was FOR (ADR 0048 decision 15, ADR 0071
-- decision 9).
--
-- cloud_cost_daily next door answers "whose". About four fifths of the bill has no whose —
-- it lands there with membership_id='' and the only breakdown is the AWS service name, so an
-- inference engine's GPU hours read as "Amazon EC2 - Compute" next to the idle slot pool, and
-- the TTS task reads as "Amazon ECS" next to the Control Plane's own. Measured on af-sandbox
-- over 2026-09-01..08, the engines were 19% of the whole bill while being invisible.
--
-- This table is the SAME MONEY cut a second way: one Cost Explorer request grouped by the
-- af-role cost allocation key, filtered to exactly the rows the shared bucket already holds.
-- It is NOT an estimate and NOT an apportionment (ADR 0048 decisions 2 and 4 both still
-- hold) — it is the invoice, grouped differently.
--
-- Two things are load-bearing:
--
--   - It covers the SHARED bucket only, and it is the same set of line items, so its total
--     equals cloud_cost_daily's shared total. Rows that carry af-membership are excluded by
--     the poller's filter, because those are already answered by "whose" — summing the two
--     tables would count a claimed slot twice.
--   - role='' is real, not missing: NAT, ALB, RDS, Route53 and tax carry no af-role and
--     never will (a NAT gateway's bytes cannot be attributed to what asked for them). It is
--     the honest residual, and naming it that way is the point.
CREATE TABLE cloud_cost_role_daily (
  day        TEXT NOT NULL,               -- YYYY-MM-DD (UTC), as Cost Explorer reports it
  role       TEXT NOT NULL,               -- af-role value. '' = carries no role at all
  service    TEXT NOT NULL,               -- AWS service name as CE returns it
  unblended  INTEGER NOT NULL DEFAULT 0,  -- micro-units — what is invoiced
  amortized  INTEGER NOT NULL DEFAULT 0,  -- micro-units — differs only with RI/Savings Plans
  currency   TEXT NOT NULL DEFAULT '',    -- as AWS returned it — never converted
  estimated  INTEGER NOT NULL DEFAULT 0,  -- the day is not final yet and WILL move
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, role, service)
);
CREATE INDEX idx_cloud_cost_role_day ON cloud_cost_role_daily(day);
