-- Schedules created or edited from a session (#4631): approval_pending=1 marks a schedule the
-- member has not approved yet. A pending row never fires and never wakes a stopped workspace,
-- because ListDueSchedules skips it. Approving it (member only) clears the mark.
-- Forward compatible: default 0 reads every existing row as approved, which is what they were.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN approval_pending INTEGER NOT NULL DEFAULT 0
