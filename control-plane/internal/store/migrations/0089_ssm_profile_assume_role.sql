-- Settings > SSM profile type "assume a role from another Settings profile" (issue #1109).
--
-- kind is sso (a profile with its own Identity Center login, which is every row written
-- before this migration) or assume_role. An assume_role row holds no portal of its own:
-- source_profile_id names the sso profile whose login starts the chain and role_arn the
-- role to assume. external_id, session_name and duration_seconds are the optional
-- assume-role parameters (0 = the AWS default). None of these is a secret.
ALTER TABLE ssm_profile ADD COLUMN kind TEXT NOT NULL DEFAULT 'sso';
ALTER TABLE ssm_profile ADD COLUMN source_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN role_arn TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN session_name TEXT NOT NULL DEFAULT '';
ALTER TABLE ssm_profile ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0
