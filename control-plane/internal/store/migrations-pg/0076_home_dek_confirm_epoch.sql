-- Postgres mirror of migrations/0091_home_dek_confirm_epoch.sql (issue #1646).
-- See the SQLite file for what the column means.
ALTER TABLE home_dek ADD COLUMN confirm_epoch INTEGER NOT NULL DEFAULT 0
