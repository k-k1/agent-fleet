-- Internal git token rotation (issue #1199), Postgres mirror of
-- migrations/0083_membership_git_token_epoch.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE membership ADD COLUMN IF NOT EXISTS git_token_epoch BIGINT NOT NULL DEFAULT 0
