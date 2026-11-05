-- Postgres mirror of migrations/0122_ssm_profile_model_route.sql (ADR 0128, issue #3282).
-- See the SQLite file for what the column means.
ALTER TABLE ssm_profile ADD COLUMN model_route INTEGER NOT NULL DEFAULT 0
