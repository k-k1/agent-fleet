-- Member removal (issue #1087): marks a schedule the scheduler paused because its owner's
-- membership was inactive at the slot. Re-inviting the person resumes exactly the rows that
-- carry it, and any change the owner makes (pause, resume, edit, run-now) clears it, so a
-- schedule the owner paused is never resumed by a restore. Default 0 = no row written
-- before this column was paused by a removal it can tell apart, which is the safe reading.
-- 0/1 integer.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN held_by_removal INTEGER NOT NULL DEFAULT 0
