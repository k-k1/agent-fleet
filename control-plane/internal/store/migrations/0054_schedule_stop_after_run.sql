-- Scheduled execution (docs/log/38) x stop-after-turn (docs/log/85): per-schedule opt-in to
-- folding the fire's session away once it has finished the prompt. stop_after_run=1 arms the
-- Agent's stop-after-turn on that session, so the stop happens after the turn ends and after
-- any report the fire owes has been delivered. Default 0 = leave the session running, the
-- behaviour every schedule written before this column was created against.
-- 0/1 integer.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN stop_after_run INTEGER NOT NULL DEFAULT 0
