-- Message queues: name what holds a run open. memo_delivery.held_detail is the JSON array of the
-- watches and timers behind a held_by of wakers at the last kept read, each one's id and source and
-- whether it is a timer or a built-in, empty when none was listed (an older Agent, or any other
-- hold). It never holds a watch's payload. The Console writes it into the run history's reason.
-- Forward compatible: one column is added with a default that means today's behaviour (no names),
-- and a binary that predates it ignores it. An old row reads as empty, and so does a row a binary
-- that predates the column wrote after this one.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN held_detail TEXT NOT NULL DEFAULT ''
