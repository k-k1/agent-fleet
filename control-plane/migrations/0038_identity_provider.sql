-- Which IdP account a person signed in with (docs/log/61 §61.5 + ADR0043 決定 4).
--
-- identity.user_key is derived from the email and is also the workspace home
-- directory name, so it must never move. This table adds the stable axis next to
-- it: an IdP subject never changes, while an email does (姓変更, ドメイン統合).
-- With the pair recorded here, a person keeps one identity — one workspace, one
-- home, one set of secrets — across an email change, and across two IdPs that
-- report the SAME email.
--
-- Deliberately NOT a way to merge two different emails into one person. Being
-- able to sign in to two accounts only proves control of both, not that they are
-- one human, and a merge cannot be undone once the workspace is shared. A login
-- whose email matches no existing identity becomes a new identity instead.
--
-- Migrating an existing deployment needs no data step: the first Google login
-- after the upgrade writes its own row against the identity the email already
-- resolves to, and user_key is untouched. Forward compatible — an older CP binary
-- ignores this table entirely.
--
-- email is the last value seen, kept for display only. Identity is decided by
-- (provider, subject) and never by this column.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
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
