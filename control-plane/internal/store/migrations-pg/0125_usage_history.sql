-- Per-member resource usage history (ADR 0139 decision 3), Postgres mirror of migrations/0140_usage_history.sql.
-- See that file for the column meanings. Forward compatible: three new tables.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
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

CREATE INDEX idx_workspace_launch_stopped ON workspace_launch(stopped_at);

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

CREATE INDEX idx_member_usage_daily_day ON member_usage_daily(day);

CREATE TABLE usage_pull_cursor (
    membership_id TEXT NOT NULL PRIMARY KEY,
    cursor_t      BIGINT NOT NULL
);
