CREATE TABLE user_limit (
    membership_id TEXT PRIMARY KEY REFERENCES membership(id),
    max_sessions  INTEGER NOT NULL DEFAULT 0,
    disk_gb       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
)
