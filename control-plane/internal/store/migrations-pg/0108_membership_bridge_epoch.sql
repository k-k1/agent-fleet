-- Bridge token epoch (issue #3452), Postgres mirror of
-- migrations/0123_membership_bridge_epoch.sql. See that file for semantics.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE membership ADD COLUMN IF NOT EXISTS bridge_epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE membership ADD COLUMN IF NOT EXISTS bridge_epoch_started BIGINT NOT NULL DEFAULT 0
