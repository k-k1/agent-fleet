-- Message queues, step 5 (ADR 0137 section 8, "Tidying before a fire"). A queue may tidy its pending
-- messages before they fire: a run merges, splits and moves them, and the person can undo it.
-- memo_queue.tidy is the switch and tidy_destinations the queue ids a run may move a message to as
-- pending, a JSON array of category ids. Both are written by the Console settings PUT only.
-- no_tidy_at is set while a fire found a workspace whose Agent cannot tidy.
-- memo.derived_from is the JSON array of source ids of a message a run made, source_provenance the
-- provenance its sources had, tidy_from the category id a run moved a message from, tidy_seen_hash
-- the content hash a run last looked at (a message whose hash differs is unseen again), tidy_run_id
-- the run that made, moved or superseded the row.
-- A source of a merge or a split stays as state superseded with sent_at set, so the retention sweep
-- collects it. The CHECK on the last column refuses a superseded row without sent_at and a run id.
-- memo_tidy_run is one row per run. input and ops hold ids, hashes and counts, never message text.
-- The unique index on a running run is what holds one run at a time per member across two Control
-- Plane processes.
-- Forward compatible: columns, one table and two indexes are added, with defaults that mean today's
-- behaviour (no queue tidies and no row is derived), and a binary that predates them ignores them.
-- A superseded row under an older binary: it is locked like a delivered one, so its PATCH and a
-- hand send are refused or settle it as delivered, and a bucket move writes it back as a sent note
-- or as delivered, never as pending, because sent_at is set. An older statement that clears sent_at
-- on a superseded row fails on the CHECK instead of reviving it. Measured by
-- TestMemoTidyOlderWriterCannotReviveASupersededRow.
-- A derived row under an older binary: provenance curator, state held or pending. The older claim
-- refuses it, so it is neither lost nor fired twice, and the older settings write and move
-- statements treat it as any other message, with its release re-recorded. Measured by
-- TestMemoTidyOlderWriterAndADerivedRow.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN tidy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN tidy_destinations TEXT NOT NULL DEFAULT '[]';
ALTER TABLE memo_queue ADD COLUMN no_tidy_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN derived_from TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN source_provenance TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN tidy_from TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN tidy_seen_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN tidy_run_id TEXT NOT NULL DEFAULT '' CHECK (state <> 'superseded' OR (sent_at <> '' AND tidy_run_id <> ''));
CREATE TABLE IF NOT EXISTS memo_tidy_run(
  id            TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL,
  queue_id      TEXT NOT NULL,
  trigger       TEXT NOT NULL,
  state         TEXT NOT NULL,
  reason        TEXT NOT NULL DEFAULT '',
  input         TEXT NOT NULL DEFAULT '',
  ops           TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  finished_at   TEXT NOT NULL DEFAULT '',
  undone_at     TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memo_tidy_run_queue ON memo_tidy_run(queue_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memo_tidy_run_running ON memo_tidy_run(membership_id) WHERE state = 'running'
