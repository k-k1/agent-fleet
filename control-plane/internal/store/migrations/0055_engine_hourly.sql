-- Hourly occupancy of the self-hosted inference engines (ADR 0071).
--
-- usage_hourly next door answers the same question about a member's WORKSPACE. It cannot
-- answer it about an engine: an engine has no membership, no tenant and no sessions, and
-- the thing worth knowing about it -- that a GPU box went on billing at 03:00 and nobody
-- was awake -- is not derivable from any row over there.
--
-- The sampler is the on-demand controller's own tick (engine_control.go). It already reads
-- the service once per interval to decide whether to start or stop it, so recording what it
-- saw costs one INSERT and no extra AWS call. Nothing else in the CP knows an engine's state
-- often enough to be a sampler.
--
-- ⚠️ THREE STATES, NOT TWO, exactly as in usage_hourly. A cell is stopped / running / never
-- observed:
--   * a row exists = the controller ticked in that hour, so the hour was OBSERVED. Unlike
--     usage_hourly this needs no separate heartbeat row: the controller watches ONE engine
--     and cannot half-observe it, whereas the workspace sweep walks every tenant and can
--     come back with a partial answer.
--   * no row = the CP was not running, the engine did not exist yet, or the controller is
--     switched off (interval 0). The hour is UNKNOWN and the UI leaves it BLANK.
-- Collapsing that to two states makes a day the control plane was down render as a
-- confident "the GPU was idle all day", which is the one thing an operator would act on.
--
-- ⚠️ observed_secs is STORED, not derived from samples x interval. The controller's interval
-- is not constant -- it drops from 30 s to 5 s while an engine is starting or warming up
-- (engineControlBusyInterval) -- so multiplying samples by the nominal interval overstates a
-- busy hour and the running ratio comes out above 100%. The denominator is only trustworthy
-- if the sampler writes down how long it can actually vouch for.
--
-- ⚠️ draining_secs is stopped-but-still-billing (ADR 0071 decision 7). ECS reports the
-- service as stopped the moment the task goes, while Managed Instances keeps the EC2
-- instance for several more minutes (measured 427-477 s on a GPU box). Folding it into
-- running_secs would claim the engine was serving, and dropping it would hide the money.
-- It gets its own column so the heatmap can show either.
--
-- It is NOT money and must never be rendered as money -- the same rule as usage_hourly, for
-- the same reason (ADR 0048 決定 2): an hourly amount could only be seconds x a rate somebody
-- typed in once. This says how long the hardware was up. It does not price it.
CREATE TABLE engine_hourly (
  engine_key    TEXT NOT NULL,               -- 'llm' / 'image' -- the engine table's key
  hour          TEXT NOT NULL,               -- YYYY-MM-DDTHH (UTC) -- the client shifts to local time
  samples       INTEGER NOT NULL DEFAULT 0,  -- controller ticks that observed this engine in the hour
  observed_secs INTEGER NOT NULL DEFAULT 0,  -- seconds those ticks can vouch for = the denominator
  running_secs  INTEGER NOT NULL DEFAULT 0,  -- of those, seconds the service had a task running
  starting_secs INTEGER NOT NULL DEFAULT 0,  -- desired 1, task not up yet (the cold start, already billing)
  draining_secs INTEGER NOT NULL DEFAULT 0,  -- desired 0, the MI box still registered (still billing)
  PRIMARY KEY (engine_key, hour)
);
CREATE INDEX idx_engine_hourly_hour ON engine_hourly(hour);
