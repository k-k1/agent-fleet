-- docs/log/20 M2: egress observation aggregation for the log-only forward proxy.
-- One row per (day, host, allowed) with an accumulated hit count. allowed=1 means the
-- policy would permit the destination and allowed=0 means it would be blocked (recorded
-- but NOT enforced in log-only mode). Attribution is deployment-wide in M2 (no per-
-- tenant column yet, see docs/log/20). Keep comments free of semicolons and quotes because
-- the migrator splits statements on the semicolon character naively.
CREATE TABLE IF NOT EXISTS egress_daily(
  day     TEXT NOT NULL,
  host    TEXT NOT NULL,
  allowed INTEGER NOT NULL,
  count   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(day, host, allowed)
);
CREATE INDEX IF NOT EXISTS idx_egress_day ON egress_daily(day);
