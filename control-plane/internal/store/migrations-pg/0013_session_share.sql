-- Postgres mirror of migrations/0030_session_share.sql.
CREATE TABLE IF NOT EXISTS session_share (
    id                      TEXT PRIMARY KEY,
    tenant_id               TEXT NOT NULL REFERENCES tenant(id),
    owner_membership_id     TEXT NOT NULL REFERENCES membership(id),
    recipient_membership_id TEXT NOT NULL REFERENCES membership(id),
    scope_type              TEXT NOT NULL CHECK (scope_type IN ('session','repo','worktree')),
    scope_key               TEXT NOT NULL,
    permission              TEXT NOT NULL CHECK (permission IN ('ro','rw')),
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    UNIQUE(owner_membership_id, recipient_membership_id, scope_type, scope_key),
    CHECK (owner_membership_id <> recipient_membership_id)
);
CREATE INDEX IF NOT EXISTS idx_session_share_recipient ON session_share(recipient_membership_id);
CREATE TABLE IF NOT EXISTS shared_session_catalog (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspace(id),
    owner_membership_id TEXT NOT NULL REFERENCES membership(id),
    name                TEXT NOT NULL,
    kind                TEXT NOT NULL,
    dir                 TEXT NOT NULL,
    repo                TEXT NOT NULL,
    working_copy_id     TEXT NOT NULL DEFAULT '',
    title               TEXT NOT NULL DEFAULT '',
    label               TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    state               TEXT NOT NULL,
    archived            INTEGER NOT NULL DEFAULT 0,
    last_seen           TEXT NOT NULL,
    UNIQUE(workspace_id, name)
);
CREATE INDEX IF NOT EXISTS idx_shared_catalog_owner ON shared_session_catalog(owner_membership_id);
CREATE TABLE IF NOT EXISTS session_share_proposal (
    id                      TEXT PRIMARY KEY,
    tenant_id               TEXT NOT NULL REFERENCES tenant(id),
    catalog_id              TEXT NOT NULL REFERENCES shared_session_catalog(id) ON DELETE CASCADE,
    owner_membership_id     TEXT NOT NULL REFERENCES membership(id),
    proposer_membership_id  TEXT NOT NULL REFERENCES membership(id),
    action                  TEXT NOT NULL CHECK (action IN ('turn','respond','answer-question','plan-respond')),
    ciphertext              TEXT NOT NULL,
    key_ref                 TEXT NOT NULL DEFAULT '',
    status                  TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','approved','rejected','expired')),
    created_at              TEXT NOT NULL,
    expires_at              TEXT NOT NULL,
    decided_at              TEXT NOT NULL DEFAULT '',
    decided_by              TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_share_proposal_owner ON session_share_proposal(owner_membership_id, status)
