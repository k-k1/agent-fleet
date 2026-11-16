-- Message queues, typed references (ADR 0137 section 4). A message may carry a pointer to a work
-- item: ref_provider (github, jira or bitbucket), ref_key (owner/repo#N or PROJ-12) and ref_url.
-- All three are empty for a note without one, and the API layer validates them, so a row never
-- holds a part of a reference. The item's body is never copied.
-- release_ref_provider, release_ref_key and release_ref_url copy the reference a pending message
-- was released with, and the CHECK on the last of them refuses any write that leaves a pending
-- message with another reference, the way the CHECK of 0131 does for the rest of the content. So a
-- Control Plane that predates this migration cannot leave a pending row under a release of other
-- content, and it cannot write a reference at all.
-- The partial unique index is the second guard of "one open message per queue and reference". The
-- API refuses a duplicate first, inside the member's lock, and answers the message already there.
-- The index is what holds when a writer skips that check. Open is held, pending or claimed: a
-- message that can still be fired, or is being fired. unconfirmed, delivered and superseded are
-- closed. The index key is the exact key: the API also folds case for the keys that tracker
-- treats as case-insensitive.
-- memo_queue.no_refs_at is set by the dispatcher or a send that found an Agent that cannot show a
-- reference. While it is set a fire leaves the messages that carry one where they are.
-- Forward compatible: columns, one index and defaults that mean today's behaviour only.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN ref_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN ref_key TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN ref_url TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_ref_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_ref_key TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_ref_url TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR (release_ref_provider = ref_provider
  AND release_ref_key = ref_key AND release_ref_url = ref_url));
CREATE UNIQUE INDEX IF NOT EXISTS idx_memo_open_ref ON memo(membership_id, repo, category, ref_provider, ref_key)
  WHERE ref_key <> '' AND state IN ('held', 'pending', 'claimed');
ALTER TABLE memo_queue ADD COLUMN no_refs_at TEXT NOT NULL DEFAULT ''
