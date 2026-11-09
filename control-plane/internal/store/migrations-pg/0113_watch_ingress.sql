-- Watch engine webhook ingress (ADR 0133 step 4, issue #3630).
-- watch_subject: the subjects a running workspace's Agent asked to be poked about. One row per
-- (membership, subject), replaced as a set by the Agent and expired when the Agent stops
-- renewing it. The row holds a subject reference and nothing the member can read with a
-- credential. tenant_id is copied from the membership so a delivery can be matched inside one
-- tenant with a single index read.
-- watch_hook: a webhook endpoint owned by one tenant. Its secret is not stored: it is derived
-- from the deployment's token signing root and the hook id, so the database holds nothing that
-- verifies a delivery.
-- Both tables start empty, so the migration is forward compatible.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS watch_subject (
    membership_id TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    source        TEXT NOT NULL,
    subject       TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    PRIMARY KEY (membership_id, subject)
);
CREATE INDEX IF NOT EXISTS idx_watch_subject_subject ON watch_subject(subject, tenant_id);
CREATE TABLE IF NOT EXISTS watch_hook (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    membership_id TEXT NOT NULL,
    source        TEXT NOT NULL,
    repo          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_watch_hook_tenant ON watch_hook(tenant_id);
