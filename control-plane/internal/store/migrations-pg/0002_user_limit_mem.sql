-- Per-workspace RAM cap (bytes) as a per-membership quota override (mirrors SQLite
-- migration 0018). 0 = unset means the deployment default. BIGINT because a byte
-- count overflows Postgres 32-bit INTEGER. Applied at the next container start.
ALTER TABLE user_limit ADD COLUMN mem_limit BIGINT NOT NULL DEFAULT 0
