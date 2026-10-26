-- Which MCP endpoint a Personal Access Token is for (#2764): 'mcp' is the full /mcp surface,
-- 'memory' is /mcp/memory, which serves the external memory tools and nothing else. Each endpoint
-- refuses the other's tokens. Every token issued before this column existed was a /mcp token.
ALTER TABLE pat ADD COLUMN resource TEXT NOT NULL DEFAULT 'mcp';
