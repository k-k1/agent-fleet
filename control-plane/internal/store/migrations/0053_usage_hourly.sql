-- Hourly occupancy, the material for the 稼働時間 heatmap (docs/log/83).
--
-- usage_daily next door answers "how many hours did this member's workspace run in
-- August". It cannot answer "were they running it overnight" or "what were they doing
-- on the day the invoice spiked", because a day is one number. This table keeps the
-- same claim at hour resolution, plus the thing usage_daily never recorded at all:
-- how many sessions were open inside that running workspace.
--
-- It is NOT money and must never be rendered as money. Cost Explorer reports per DAY,
-- so an hourly figure could only be an estimate (seconds x a rate somebody typed once),
-- and ADR 0048 決定 2 refused exactly that. The heatmap explains a day's cost by showing
-- what the workspace was doing -- it does not price it.
--
-- ⚠️ THREE STATES, NOT TWO. A cell is stopped / running / never observed, and they must
-- stay distinguishable:
--   * membership_id = '' is the SAMPLER HEARTBEAT for the hour (same convention as
--     cloud_cost_daily's shared bucket). samples counts the sweeps that completed.
--     No heartbeat row = the CP was not running (or predates this table) = the hour is
--     UNKNOWN and the UI leaves it blank.
--   * heartbeat present, member row absent = observed and NOT running = grey.
-- Without the heartbeat, a control plane that was down for a day would render as a
-- confident "this member ran nothing", which is the 0-vs-unmeasured mistake that
-- usage accounting keeps re-teaching.
--
-- ⚠️ measured_secs is the same distinction one level down. A workspace can be running
-- while its Agent is unreachable (mid-start, wedged), and counting that as "0 sessions"
-- would paint an idle-looking cell over a busy hour. Session counters accumulate only
-- over measured_secs, so an hour with running_secs > 0 and measured_secs = 0 reads as
-- "running, session count unknown" rather than as zero.
CREATE TABLE usage_hourly (
  membership_id TEXT NOT NULL,               -- '' = sampler heartbeat for this hour
  tenant_id     TEXT NOT NULL,               -- '' on the heartbeat row
  hour          TEXT NOT NULL,               -- YYYY-MM-DDTHH (UTC) -- the client shifts to local time
  samples       INTEGER NOT NULL DEFAULT 0,  -- sweeps that visited this row in the hour
  running_secs  INTEGER NOT NULL DEFAULT 0,  -- seconds the workspace was running
  measured_secs INTEGER NOT NULL DEFAULT 0,  -- of those, seconds the session list was actually read
  session_secs  INTEGER NOT NULL DEFAULT 0,  -- sum of (alive sessions x secs) over measured samples
  busy_secs     INTEGER NOT NULL DEFAULT 0,  -- sum of (machine-busy sessions x secs), same samples
  max_sessions  INTEGER NOT NULL DEFAULT 0,  -- peak alive count seen in the hour
  max_busy      INTEGER NOT NULL DEFAULT 0,  -- peak machine-busy count seen in the hour
  PRIMARY KEY (membership_id, hour)
);
CREATE INDEX idx_usage_hourly_tenant_hour ON usage_hourly(tenant_id, hour);
CREATE INDEX idx_usage_hourly_hour ON usage_hourly(hour);
