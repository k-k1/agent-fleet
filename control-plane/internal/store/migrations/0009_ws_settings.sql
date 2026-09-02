-- Per-workspace member settings owned by the Control Plane (not the Agent) so they
-- can be edited while the container is stopped and mapped to env at start (see
-- workspaceExtraEnv). JSON blob, empty means no overrides. New settings become JSON
-- fields rather than new columns.
ALTER TABLE workspace ADD COLUMN settings TEXT NOT NULL DEFAULT '';
