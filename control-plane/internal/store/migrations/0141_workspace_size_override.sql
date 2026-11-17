-- ADR 0139 decisions 4 and 5. A per-member size override that sits BESIDE the stored choice in
-- user_limit and never replaces it: source is 'boost' (a member-pressed, time-limited rung up) or
-- 'auto' (the right-sizing recommender, not built yet). One row per (membership, source), so a boost
-- and an auto override have their own lifetimes. slot_class and mem_mib are the target box
-- (the rung's declared memory), re-validated on every start. expires_at is RFC3339 UTC, empty for no end.
-- Forward compatible: a new table nothing reads before this binary.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE workspace_size_override (
    membership_id TEXT NOT NULL,
    source        TEXT NOT NULL,
    slot_class    TEXT NOT NULL DEFAULT '',
    mem_mib       INTEGER NOT NULL DEFAULT 0,
    expires_at    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    PRIMARY KEY (membership_id, source)
)
