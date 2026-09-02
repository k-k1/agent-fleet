-- Showback usage accounting (docs/roadmap.md P3-9, history/p3-9-showback.md).
-- The infra cost being showed-back in the BYO model is workspace occupancy.
-- Claude usage is each user's own subscription and not counted here. What costs
-- the operator RAM/CPU (or Fargate hours on AWS) is how long each workspace runs.
-- A background sampler adds one interval of seconds to the running workspace's
-- daily bucket each sweep, so running_secs approximates occupancy to within one
-- sample interval (approximate is fine for internal use, no external billing).
CREATE TABLE usage_daily (
  membership_id TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  day           TEXT NOT NULL,               -- YYYY-MM-DD (UTC)
  running_secs  INTEGER NOT NULL DEFAULT 0,  -- accumulated workspace-running seconds
  PRIMARY KEY (membership_id, day)
);
CREATE INDEX idx_usage_tenant_day ON usage_daily(tenant_id, day);
