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

CREATE TABLE workspace (
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
);

CREATE TABLE user_limit (
    membership_id TEXT PRIMARY KEY REFERENCES membership(id),
    max_sessions  INTEGER NOT NULL DEFAULT 0,
    disk_gb       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE TABLE wrapped_dek (
    workspace_id TEXT PRIMARY KEY REFERENCES workspace(id),
    ciphertext   TEXT NOT NULL,
    key_ref      TEXT NOT NULL,
    key_version  INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
);

CREATE TABLE session (
    workspace_id TEXT NOT NULL REFERENCES workspace(id),
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    dir          TEXT NOT NULL,
    repo         TEXT NOT NULL,
    label        TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    state        TEXT NOT NULL,
    last_seen    TEXT NOT NULL,
    PRIMARY KEY (workspace_id, name)
);

CREATE TABLE pat (
    id            TEXT PRIMARY KEY,
    identity_id   TEXT NOT NULL REFERENCES identity(id),
    membership_id TEXT REFERENCES membership(id),
    token_hash    TEXT NOT NULL UNIQUE,
    scope         TEXT NOT NULL DEFAULT 'read',
    name          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    expires_at    TEXT,
    revoked_at    TEXT,
    last_used_at  TEXT
);

CREATE INDEX idx_pat_identity ON pat(identity_id);

CREATE TABLE audit_log (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL DEFAULT '',
    actor_kind TEXT NOT NULL,
    actor_id   TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL
);

CREATE INDEX idx_audit_tenant_at ON audit_log(tenant_id, at);

CREATE TABLE usage_daily (
    membership_id TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    day           TEXT NOT NULL,
    running_secs  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (membership_id, day)
);

CREATE INDEX idx_usage_tenant_day ON usage_daily(tenant_id, day);

CREATE TABLE ssm_profile (
    id            TEXT PRIMARY KEY,
    membership_id TEXT NOT NULL,
    label         TEXT NOT NULL,
    start_url     TEXT NOT NULL DEFAULT '',
    sso_region    TEXT NOT NULL DEFAULT '',
    account_id    TEXT NOT NULL DEFAULT '',
    role_name     TEXT NOT NULL DEFAULT '',
    region        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_ssm_profile_membership ON ssm_profile(membership_id);

CREATE TABLE ssm_host (
    id             TEXT PRIMARY KEY,
    membership_id  TEXT NOT NULL,
    alias          TEXT NOT NULL,
    sso_session_id TEXT NOT NULL DEFAULT '',
    account_id     TEXT NOT NULL DEFAULT '',
    role_name      TEXT NOT NULL DEFAULT '',
    region         TEXT NOT NULL DEFAULT '',
    instance_id    TEXT NOT NULL,
    document_name  TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    profile_id     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_ssm_host_membership ON ssm_host(membership_id);

CREATE TABLE egress_allowlist (
    id        TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT '',
    entry     TEXT NOT NULL,
    state     TEXT NOT NULL DEFAULT 'active',
    reason    TEXT NOT NULL DEFAULT '',
    added_by  TEXT NOT NULL DEFAULT '',
    added_at  TEXT NOT NULL
);

CREATE INDEX idx_egress_allow ON egress_allowlist(tenant_id, state);

CREATE TABLE egress_daily (
    day     TEXT NOT NULL,
    host    TEXT NOT NULL,
    allowed INTEGER NOT NULL,
    count   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, host, allowed)
);

CREATE INDEX idx_egress_day ON egress_daily(day);

CREATE TABLE deployment_setting (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- Internal git provider (ledger + LFS) and memo queue (added in SQLite migrations
-- 0014-0017, consolidated here for fresh Postgres deployments).
CREATE TABLE git_repo (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenant(id),
    name           TEXT NOT NULL,
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_by     TEXT,
    created_at     TEXT NOT NULL,
    UNIQUE(tenant_id, name)
);

CREATE INDEX idx_git_repo_tenant ON git_repo(tenant_id);

CREATE TABLE lfs_object (
    tenant_id  TEXT    NOT NULL REFERENCES tenant(id),
    repo_name  TEXT    NOT NULL,
    oid        TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    created_at TEXT    NOT NULL,
    PRIMARY KEY (tenant_id, repo_name, oid)
);

CREATE INDEX idx_lfs_object_tenant ON lfs_object(tenant_id);

CREATE TABLE lfs_lock (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id),
    repo_name  TEXT NOT NULL,
    path       TEXT NOT NULL,
    ref_name   TEXT NOT NULL DEFAULT '',
    owner_id   TEXT NOT NULL,
    owner_name TEXT NOT NULL DEFAULT '',
    locked_at  TEXT NOT NULL,
    UNIQUE(tenant_id, repo_name, path)
);

CREATE INDEX idx_lfs_lock_repo ON lfs_lock(tenant_id, repo_name);

CREATE TABLE memo (
    id            TEXT PRIMARY KEY,
    membership_id TEXT NOT NULL,
    repo          TEXT NOT NULL DEFAULT '',
    category      TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    ref_path      TEXT NOT NULL DEFAULT '',
    position      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    sent_at       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_memo_membership_repo ON memo(membership_id, repo);
