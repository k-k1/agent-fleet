CREATE TABLE identity (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL DEFAULT '',
    user_key      TEXT UNIQUE NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user',
    status        TEXT NOT NULL DEFAULT 'active',
    last_login_at TEXT
);

CREATE UNIQUE INDEX idx_identity_email ON identity(email) WHERE email <> '';

CREATE TABLE membership (
    id          TEXT PRIMARY KEY,
    identity_id TEXT NOT NULL REFERENCES identity(id),
    tenant_id   TEXT NOT NULL REFERENCES tenant(id),
    role        TEXT NOT NULL DEFAULT 'member',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    UNIQUE(identity_id, tenant_id)
);

CREATE TABLE workspace_new (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenant(id),
    membership_id  TEXT NOT NULL UNIQUE REFERENCES membership(id),
    container_name TEXT NOT NULL,
    network        TEXT NOT NULL,
    data_dir       TEXT NOT NULL,
    agent_port     TEXT NOT NULL,
    agent_token    TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'stopped',
    created_at     TEXT NOT NULL,
    last_active_at TEXT
)
