-- Message queues, a start time a message carries (ADR 0137, addendum 2026-11-22, #4777).
-- memo.start_after is the earliest time a message may be claimed (fixed-width UTC, the format of
-- memo_delivery's stamps), empty for none. Unlike not_before, which is a run's hand-back and which
-- a release clears, it is content: the person or the session that wrote the message set it, and a
-- release is bound to it.
-- release_start_after copies the time a pending message was released with, and the CHECK refuses
-- any write that leaves a pending message with another time, as the CHECKs of 0131, 0138 and 0144
-- do for the rest of the content. So a Control Plane that predates this migration cannot leave a
-- pending row under a release of another time.
-- Forward compatible: columns with defaults that mean today's behaviour. A binary that predates
-- them never waits for start_after, and an old row reads as a message with no start time.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN start_after TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_start_after TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_start_after = start_after)
