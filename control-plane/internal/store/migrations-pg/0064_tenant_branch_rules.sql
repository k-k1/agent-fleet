-- A tenant's branch naming rules (ADR 0103 decision 10), the tenant layer the Workspace Agent
-- polls from GET /internal/branch-rules. One row per tenant holding the whole list as JSON,
-- because the admin saves the list as a whole and the Agent reads it as a whole, and the rule
-- shape is the Agent's (match, name, base, types) rather than anything the CP queries by.
-- No row means the tenant has no rules, so an upgraded deployment changes nothing.
CREATE TABLE IF NOT EXISTS tenant_branch_rules(
  tenant_id  TEXT PRIMARY KEY,
  rules      TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
