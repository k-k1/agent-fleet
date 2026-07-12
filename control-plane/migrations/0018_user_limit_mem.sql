-- Per-workspace RAM cap (bytes) as a per-membership quota override. 0 = unset means
-- the deployment default (WS_MEMORY / AF_ECS_TASK_MEMORY). A tenant_admin sets it
-- within the tenant cap (tenant.limits.max_workspace_mem) and it is applied at the
-- next container start (docker memory flag / ECS task size). SQLite INTEGER is 64-bit.
ALTER TABLE user_limit ADD COLUMN mem_limit INTEGER NOT NULL DEFAULT 0
