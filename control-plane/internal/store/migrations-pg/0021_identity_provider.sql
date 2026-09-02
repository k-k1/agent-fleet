-- Postgres mirror of migrations/0038_identity_provider.sql.
CREATE TABLE IF NOT EXISTS identity_provider (
    provider      TEXT NOT NULL,
    subject       TEXT NOT NULL,
    identity_id   TEXT NOT NULL REFERENCES identity(id),
    email         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    last_login_at TEXT,
    PRIMARY KEY (provider, subject)
);

CREATE INDEX IF NOT EXISTS idx_identity_provider_identity ON identity_provider(identity_id)
