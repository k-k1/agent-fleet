-- Which CLIs failed to install on the workspace's last start, stored beside the disk reading
-- because the usage sampler takes both from the same Agent call and the tenant member roster
-- reads both from the DB alone. A JSON object of tool to reason, for example
-- {"claude":"no_space"}. An empty string means no failure was reported, which also covers an
-- Agent that does not say.
-- Forward compatible: a new column with a default, and an older Control Plane that starts
-- against this schema never names it. No backfill, the next sample fills it.
ALTER TABLE workspace_disk ADD COLUMN boot_failed TEXT NOT NULL DEFAULT ''
