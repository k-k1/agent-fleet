-- Per-member resource usage history (ADR 0139 decision 3, issue 4333).
--
-- workspace_launch: one row per container launch, written by the CP itself. Running time comes
-- from here and never from the Agent's samples. instance_type is the box the runtime placed the
-- launch on, override names what chose it ('' = nothing overrode the member's own setting).
-- vcpu and baseline_pct are what the ladder declared for that rung, 0 = not declared. stopped_at
-- is NULL while the launch is open. last_seen_at is the last sampler tick that saw it running
-- and is where an unrecorded stop is closed. ran_through is how far its seconds have already
-- been folded into member_usage_daily, which is what makes folding idempotent.
--
-- member_usage_daily: one row per member, per UTC day, per box actually run on. Every measured_
-- column counts minutes the Agent actually measured that signal, so a minute it could not read
-- is a running minute and not a measured one. The three *_hist columns are bucket counts
-- (usagehist), not raw minutes. NULL on a max or on burst_min means no measurement, never zero.
--
-- usage_pull_cursor: how far the Agent's minute series has been folded for a member (the
-- Agent's own unix-second cursor).
--
-- Forward compatible: three new tables and nothing existing is touched. The migrator splits on
-- the semicolon, so comments must not contain one.
CREATE TABLE workspace_launch (
    id            TEXT NOT NULL PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    membership_id TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    started_at    BIGINT NOT NULL,
    stopped_at    BIGINT,
    last_seen_at  BIGINT NOT NULL,
    ran_through   BIGINT NOT NULL,
    instance_type TEXT NOT NULL DEFAULT '',
    class_id      TEXT NOT NULL DEFAULT '',
    override      TEXT NOT NULL DEFAULT '',
    vcpu          INTEGER NOT NULL DEFAULT 0,
    baseline_pct  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_workspace_launch_member ON workspace_launch(membership_id, started_at);

CREATE TABLE member_usage_daily (
    membership_id    TEXT NOT NULL,
    day              TEXT NOT NULL,
    instance_type    TEXT NOT NULL DEFAULT '',
    class_id         TEXT NOT NULL DEFAULT '',
    override         TEXT NOT NULL DEFAULT '',
    tenant_id        TEXT NOT NULL,
    vcpu             INTEGER NOT NULL DEFAULT 0,
    baseline_pct     INTEGER NOT NULL DEFAULT 0,
    running_secs     INTEGER NOT NULL DEFAULT 0,
    cpu_measured_min     INTEGER NOT NULL DEFAULT 0,
    ws_measured_min      INTEGER NOT NULL DEFAULT 0,
    psi_measured_min     INTEGER NOT NULL DEFAULT 0,
    refault_measured_min INTEGER NOT NULL DEFAULT 0,
    cpu_hist         TEXT NOT NULL DEFAULT '',
    ws_hist          TEXT NOT NULL DEFAULT '',
    refault_hist     TEXT NOT NULL DEFAULT '',
    cpu_max          DOUBLE PRECISION,
    ws_max           BIGINT,
    psi_full_max     DOUBLE PRECISION,
    psi_warn_min     INTEGER NOT NULL DEFAULT 0,
    burst_min        INTEGER,
    PRIMARY KEY (membership_id, day, instance_type, class_id, override)
);

CREATE INDEX idx_member_usage_daily_tenant ON member_usage_daily(tenant_id, day);

CREATE TABLE usage_pull_cursor (
    membership_id TEXT NOT NULL PRIMARY KEY,
    cursor_t      BIGINT NOT NULL
)
