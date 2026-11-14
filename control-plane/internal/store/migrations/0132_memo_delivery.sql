-- Message queues, step 1c (ADR 0137 section 6, issue 4163). A delivery is one fire of a queue:
-- the messages it claimed, the session it started and its state (claimed, sending, open,
-- closed) with the outcome of a closed one (clean, failed, aborted, unknown, not_sent). Every
-- delivery that is not closed occupies one of its queue's max_active slots and counts toward
-- the member-wide limit. A slot is reserved under the membership row's lock (MemoTx), which is
-- what keeps two Control Planes from reserving the same free slot. Each move of a delivery's
-- state is a conditional write on its own row.
-- sent_at is when the create was let go (claimed to sending), idle_ticks how many consecutive
-- scheduler ticks saw its session idle, kept here so that a restart does not reset the count.
-- message_ids is the JSON array of the memos the delivery claimed, the ledger the Console shows
-- after the memos themselves are swept.
-- memo.delivery_id is the delivery a message was claimed by, empty for any other.
-- Forward compatible: a table and a column are only added, with defaults that mean today's
-- behaviour (no delivery, no message claimed), and a binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS memo_delivery(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  state         TEXT NOT NULL,
  outcome       TEXT NOT NULL DEFAULT '',
  session       TEXT NOT NULL DEFAULT '',
  message_ids   TEXT NOT NULL DEFAULT '[]',
  idle_ticks    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  sent_at       TEXT NOT NULL DEFAULT '',
  opened_at     TEXT NOT NULL DEFAULT '',
  closed_at     TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_ms ON memo_delivery(membership_id, state);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_queue ON memo_delivery(queue_id, created_at);
CREATE INDEX IF NOT EXISTS idx_memo_delivery_state ON memo_delivery(state, created_at);
ALTER TABLE memo ADD COLUMN delivery_id TEXT NOT NULL DEFAULT ''
