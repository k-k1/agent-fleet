-- ADR 0139 decisions 4 and 5, Postgres mirror of migrations/0141_workspace_size_override.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE workspace_size_override (
    membership_id TEXT NOT NULL,
    source        TEXT NOT NULL,
    slot_class    TEXT NOT NULL DEFAULT '',
    mem_mib       BIGINT NOT NULL DEFAULT 0,
    expires_at    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    PRIMARY KEY (membership_id, source)
)
