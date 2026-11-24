-- The tenant-wide boost hours read (issue #4417): TenantOverrideRunningSecs looks for the launches of
-- one override across a whole tenant, which the membership index cannot narrow. Boosted launches are
-- a small share of all launches, so this index keeps that read to them.
-- Purely additive. A binary that predates it reads the same rows without it.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE INDEX IF NOT EXISTS idx_workspace_launch_override ON workspace_launch(override, started_at)
