-- Postgres mirror of migrations/0105_workspace_disk_boot_failed.sql.
-- See the SQLite file for what the column holds and why it is forward compatible.
ALTER TABLE workspace_disk ADD COLUMN boot_failed TEXT NOT NULL DEFAULT ''
