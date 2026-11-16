-- Message queues, step 4 (ADR 0137 section 3). memo_queue.accept_own_sessions is the per-queue
-- switch "accept from sessions I started": with it on, a message from a session whose origin is
-- user enters pending instead of held. Off by default, written by the Console settings PUT only.
-- memo.released_by records who made a pending message pending: empty for a press or a Console
-- write, setting for the switch. It is cleared whenever release_hash is.
-- Forward compatible: two columns whose defaults mean today's behaviour.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN accept_own_sessions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN released_by TEXT NOT NULL DEFAULT ''
