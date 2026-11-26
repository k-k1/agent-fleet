-- Postgres mirror of migrations/0170_handoff_source_space.sql. The migrator splits on the semicolon, so comments must not contain one.
-- Whether a member handoff came from a session running in a space (#5233). Additive. A row written before this has no origin recorded, which reads as a repository handoff.
ALTER TABLE session_handoff_offer ADD COLUMN source_space INTEGER NOT NULL DEFAULT 0;
