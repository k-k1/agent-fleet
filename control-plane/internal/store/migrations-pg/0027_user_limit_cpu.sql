-- Per-workspace CPU cap in Fargate CPU units (1024 = 1 vCPU). 0 = unset means the
-- deployment default (AF_ECS_TASK_CPU), and on the docker runtime it maps to --cpus.
-- Held as its own axis rather than being derived from mem_limit so "8 GB with 4 vCPU"
-- is expressible. fargateSize() still snaps the (cpu, memory) pair onto a valid
-- Fargate combination, so an odd value can never reach a task definition (ADR 0044).
ALTER TABLE user_limit ADD COLUMN cpu_limit INTEGER NOT NULL DEFAULT 0
