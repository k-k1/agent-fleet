-- Scheduled execution P6 (docs/log/38 + ADR0021): long-lived session reuse mode. When a
-- schedule runs with session_mode=reuse it sends each fire's prompt into the SAME
-- long-lived session (send_to_session) instead of creating a fresh one, so the
-- conversation context carries across fires. reuse_session is the ledger of which real
-- session the scheduler is currently driving (it changes when the session rotates);
-- reuse_started_at is when that current session began and reuse_run_count is how many
-- fires it has taken since the last rotation, both feeding the rotation triggers.
-- rotation is a JSON blob of rotation triggers so new triggers need no migration, e.g.
-- {"every_runs":20,"after":"7d","calendar":"weekly"} (OR-composed, empty means never).
-- missing_target_policy decides what a pinned reuse does when its target session is gone
-- ('recreate' default = make a fresh one, 'fail' = surface a failure notification).
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN reuse_session TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN reuse_started_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN reuse_run_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule ADD COLUMN rotation TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN missing_target_policy TEXT NOT NULL DEFAULT 'recreate';
