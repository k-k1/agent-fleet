-- Scheduled execution (docs/log/38 + ADR0021): richer run history. session records which
-- session each fire drove so the Console can open it — the freshly created session for
-- session_mode=new or the long-lived reuse target for session_mode=reuse, empty for soft
-- skips that ran nothing. trigger_kind distinguishes a manual run-now fire (value manual)
-- from an automatic scheduled fire (value scheduled, the default) so history tells them apart.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule_run ADD COLUMN session TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_run ADD COLUMN trigger_kind TEXT NOT NULL DEFAULT 'scheduled'
