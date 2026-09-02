CREATE TABLE tenant (
    id         TEXT PRIMARY KEY,
    slug       TEXT UNIQUE NOT NULL,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active',
    limits     TEXT NOT NULL DEFAULT '{}',
    isolation  TEXT NOT NULL DEFAULT 'shared',
    key_ref    TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE app_user (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenant(id),
    email         TEXT NOT NULL DEFAULT '',
    user_key      TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'member',
    status        TEXT NOT NULL DEFAULT 'active',
    last_login_at TEXT,
    UNIQUE(tenant_id, user_key)
);

CREATE TABLE workspace (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenant(id),
    user_id        TEXT NOT NULL UNIQUE REFERENCES app_user(id),
    container_name TEXT NOT NULL,
    network        TEXT NOT NULL,
    data_dir       TEXT NOT NULL,
    agent_port     TEXT NOT NULL,
    agent_token    TEXT NOT NULL,
    state          TEXT NOT NULL DEFAULT 'stopped',
    created_at     TEXT NOT NULL,
    last_active_at TEXT
)
