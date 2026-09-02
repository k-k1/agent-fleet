-- Scheduled execution run history (docs/log/38 + ADR0021, P3 get_schedule_runs). One row
-- per fire attempt, appended by the scheduler and trimmed to a bounded tail per schedule
-- so a frequent interval schedule cannot grow it without limit. status mirrors the
-- schedule's last_status token (fired / skipped_* / error-...) and detail is an optional
-- short note. Ordered by fired_at (RFC3339 sorts chronologically) for the newest-first list.
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
