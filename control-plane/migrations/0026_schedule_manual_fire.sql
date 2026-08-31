-- Scheduled execution (docs/log/38 + ADR0021): run-now provenance. manual_fire_pending is a
-- transient flag set when an operator/Console triggers run-now (which just sets next_run to
-- now so the fire goes through the same ticker path as an automatic fire). The scheduler
-- reads it on the next fire to tag that run's history row as manual, then clears it. 0/1.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN manual_fire_pending INTEGER NOT NULL DEFAULT 0
