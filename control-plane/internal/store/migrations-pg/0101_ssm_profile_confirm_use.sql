-- Postgres mirror of migrations/0116_ssm_profile_confirm_use.sql (ADR 0126, issue #3050).
-- See the SQLite file for what the column means.
ALTER TABLE ssm_profile ADD COLUMN confirm_use INTEGER NOT NULL DEFAULT 0
