-- Tenant-wide switch for AF memory reached from outside Agent Fleet through the CP's /mcp
-- endpoint (#2318, ADR 0108). 1 = members may opt in with their own externalMemory setting,
-- 0 = the memory tools are neither listed nor callable for anyone in the tenant. Forward
-- compatible: every existing tenant keeps 1, which is the behaviour before this column, and the
-- member-level setting stays off by default.
ALTER TABLE tenant ADD COLUMN external_memory INTEGER NOT NULL DEFAULT 1
