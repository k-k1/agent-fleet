-- Postgres mirror of migrations/0167_push_release_token.sql. The migrator splits on the semicolon, so comments must not contain one.
-- Registration-bound release token for Web Push (#4866). Additive. A row written before this has an empty token, which no token release matches.
ALTER TABLE push_subscription ADD COLUMN release_token TEXT NOT NULL DEFAULT '';
