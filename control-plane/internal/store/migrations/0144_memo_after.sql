-- Message queues, dependencies between messages (ADR 0137, addendum 2026-11-19, part A, step 1).
-- memo.after_ids is the JSON array of the ids of the messages of the same queue this message waits
-- for, empty for none. The API resolves a reference key to an id at write time, so the column only
-- ever holds ids of rows that came before this one in the queue's order.
-- release_after_ids copies the list a pending message was released with, and the CHECK refuses any
-- write that leaves a pending message with another list, as the CHECKs of 0131 and 0138 do for the
-- rest of the content. So a Control Plane that predates this migration cannot leave a pending row
-- under a release of another list, and it cannot write a list at all.
-- memo.held_reason is why the dispatcher put a message back to held because a prerequisite can no
-- longer be satisfied (JSON, the prerequisite and the reason), empty otherwise. Every write that
-- leaves held clears it.
-- Forward compatible: columns with defaults that mean today's behaviour only. A binary that
-- predates them ignores them, and an old row reads as a message with no prerequisites.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN after_ids TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN held_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_after_ids TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_after_ids = after_ids)
