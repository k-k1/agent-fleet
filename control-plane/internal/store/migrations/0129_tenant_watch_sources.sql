-- Tenant-wide switch for member-added watch sources (ADR 0133 §8, #3631). 1 = a member may mark
-- an MCP server of their own as a source for the watch engine, 0 = no member can, and the
-- Workspace Agent runs none. Forward compatible: every existing tenant gets 0, which is the
-- behaviour before this column (the feature does not exist).
ALTER TABLE tenant ADD COLUMN watch_sources INTEGER NOT NULL DEFAULT 0
