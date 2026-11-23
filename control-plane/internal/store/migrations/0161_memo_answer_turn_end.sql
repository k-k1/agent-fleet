-- Message queues, the turn the last-answer read is pinned to (issue #4891). close_turn_end_at is
-- the end of the root session's last turn (lastTurnEndAt, the Agent's clock) as the read of the
-- Agent behind the delivery's close saw it, "" when that read showed none. It is written with
-- every kept read while the delivery is open, so a closed delivery keeps the one its close was
-- based on. The answer read drops every turn a person started after it, so a session resumed
-- after the close is not read for the run's answer.
-- Forward compatible: a column with a default. A binary that predates it ignores it and reads
-- the answer without the pin, as it did.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN close_turn_end_at TEXT NOT NULL DEFAULT ''
