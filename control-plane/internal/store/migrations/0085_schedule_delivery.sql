-- Per-schedule delivery targets and the silent sentinel (issue #1560, ADR 0021 note of
-- 2026-10-03). deliver_to is a comma-separated subset of operator, notifications, discord
-- and slack. Empty = operator alone, which is what report=1 meant before this column.
-- silent=1 lets a run whose final answer is exactly [SILENT] deliver nothing.
-- Default 0 = every answer is delivered, as before. 0/1 integer.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN deliver_to TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN silent INTEGER NOT NULL DEFAULT 0
