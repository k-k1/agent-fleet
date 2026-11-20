-- Message queues, a bounded continuation of a released message (ADR 0137, addendum 2026-11-20, #4606).
-- memo_queue.continuation_max is how many continuations of one released message a run of the queue
-- may add without a release press. It defaults to 4 for every queue, existing ones included, and
-- 0 turns it off. The Control Plane clamps it to its own ceiling of 10 when it reads it.
-- memo.chain_id is the root message of the chain a continuation belongs to, chain_seq its place in
-- it and chain_delivery the delivery whose run added it. They are display and bookkeeping, and
-- empty for every other message. A person's release or a move to another category clears them.
-- memo_chain holds the count, keyed by the root message's id, so that it outlives the sweep that
-- deletes a delivered message. admitted is the continuations let in, stopped is set by the
-- member's Stop chain.
-- memo_delivery.continuation_taken is set once by the first continuation a delivery admits, in the
-- same statement that checks the delivery is still open and not overdue. It outlives the child.
-- Forward compatible: columns and a table are only added. A binary that predates them ignores
-- them and holds every continuation as it does today.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN continuation_max INTEGER NOT NULL DEFAULT 4;
ALTER TABLE memo ADD COLUMN chain_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN chain_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo ADD COLUMN chain_delivery TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN continuation_taken INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS memo_chain(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  category_id   TEXT NOT NULL,
  ref_provider  TEXT NOT NULL DEFAULT '',
  ref_key       TEXT NOT NULL DEFAULT '',
  admitted      INTEGER NOT NULL DEFAULT 0,
  stopped       INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
)
