-- Scheduled runs dropped before they ran (issue #1257): the slot a run fired for, RFC 3339 UTC,
-- so the Agent's not-executed report names exactly one run. Matching by fired_at instead picks
-- the next run of the same session when the report is repeated or the right run was trimmed.
-- Default empty = a run recorded before this column, which no report can name.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule_run ADD COLUMN slot TEXT NOT NULL DEFAULT ''
