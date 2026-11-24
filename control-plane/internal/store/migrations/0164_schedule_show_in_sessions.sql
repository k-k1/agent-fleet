-- Per-schedule "Also show in the Sessions view" (issue #5070, ADR 0140 note of 2026-11-24).
-- show_in_sessions=1 treats the worktree children a scheduled run starts as the member's own
-- in the Console's repository tree and Sessions list. Default 0 = they stay in the Schedules
-- view only, as before. 0/1 integer. Purely additive.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN show_in_sessions INTEGER NOT NULL DEFAULT 0
