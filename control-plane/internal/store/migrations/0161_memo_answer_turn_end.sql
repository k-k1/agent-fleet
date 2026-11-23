-- Message queues, the turn the last-answer read is pinned to (issue #4891). close_turn_end_at is
-- the end of the root session's last turn (lastTurnEndAt, the Agent's clock) as a kept read of the
-- Agent saw it, "" when that read showed none, and close_turn_gen that read's number (held_gen).
-- Both are written with every kept read while the delivery is open. The pin holds only while
-- close_turn_gen equals held_gen: a read kept by a binary that predates the columns moves held_gen
-- alone, and the answer is then read without the pin. The answer read drops every turn written
-- after the pin, so a session resumed after the close is not read for the run's answer.
-- Forward compatible: columns with defaults. A binary that predates them ignores them and reads
-- the answer without the pin, as it did.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN close_turn_end_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN close_turn_gen INTEGER NOT NULL DEFAULT 0
