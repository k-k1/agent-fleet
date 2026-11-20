-- Message queues, a run that hands its message back (ADR 0137, addendum 2026-11-19, part B).
-- memo.not_before is the earliest time a returned message may be claimed again (fixed-width UTC,
-- the format of memo_delivery's stamps), empty for none. memo.defer_count is how many times the
-- message was handed back since a person last released or returned it, and memo.defer_reasons the
-- JSON array of those hand-backs (reason, when, by which run). A person's release and a return to
-- the queue clear all three.
-- memo_delivery.defer_reason, defer_not_before and defer_at are the request the delivery's session
-- made with defer_queue_item (defer_at by the Agent's clock, the clock its turn starts are read
-- by), all empty for none. defer_result is what the close made of it (applied, or why it was
-- dropped), return_blocked the JSON array of the messages that stayed where they were because
-- another message of the queue holds their reference, and returned_at when a person returned the
-- run's messages to the queue.
-- Forward compatible: columns with defaults that mean today's behaviour. A binary that predates
-- them never claims by not_before, and an old row reads as a message never handed back.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN not_before TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN defer_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN defer_reasons TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN defer_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN defer_not_before TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN defer_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN defer_result TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN return_blocked TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN returned_at TEXT NOT NULL DEFAULT ''
