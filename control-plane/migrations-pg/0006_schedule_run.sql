-- Scheduled execution run history (docs/log/38 + ADR0021), Postgres mirror of
-- migrations/0023_schedule_run.sql. Dialect-neutral DDL, see that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS schedule_run(
  id            TEXT PRIMARY KEY,
  schedule_id   TEXT NOT NULL,
  membership_id TEXT NOT NULL,
  fired_at      TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT '',
  detail        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_schedule_run_sched ON schedule_run(schedule_id, fired_at)
