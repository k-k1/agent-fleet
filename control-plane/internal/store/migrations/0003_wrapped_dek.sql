CREATE TABLE wrapped_dek (
    workspace_id TEXT PRIMARY KEY REFERENCES workspace(id),
    ciphertext   TEXT NOT NULL,
    key_ref      TEXT NOT NULL,
    key_version  INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
)
