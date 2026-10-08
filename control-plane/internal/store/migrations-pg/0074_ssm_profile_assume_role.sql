-- Postgres mirror of migrations/0089_ssm_profile_assume_role.sql (issue #1109).
-- See the SQLite file for what each column means.
ALTER TABLE ssm_profile ADD COLUMN kind TEXT NOT NULL DEFAULT 'sso';
ALTER TABLE ssm_profile ADD COLUMN source_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN role_arn TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN session_name TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0
