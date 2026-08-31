-- Scheduled execution (docs/log/38): per-schedule completion-report opt-in. report=1 passes
-- the schedule's owner_conv as report_to on a fire so the session's completion rides the
-- docs/log/30 seam back to the operator/assistant conversation. Default 0 = do not report
-- (fires run silently and only the run history / failure notifications surface them).
-- 0/1 integer.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN report INTEGER NOT NULL DEFAULT 0
