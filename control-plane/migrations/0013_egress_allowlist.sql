-- docs/log/20 M3: versioned egress allowlist plus a tiny deployment key-value store.
-- egress_allowlist holds one host or dot-suffix per row, scoped global (empty tenant_id)
-- or to a tenant, with a lifecycle state of active proposed or retired. proposed rows
-- come from the M4 agent and go active only on explicit admin approval. added_by records
-- who created the row (admin email or the proposing PAT). deployment_setting is a small
-- bag for deployment-wide toggles such as the egress mode (log-only or enforce). Keep
-- comments free of semicolons and quotes because the migrator splits on the semicolon.
CREATE TABLE IF NOT EXISTS egress_allowlist(
  id        TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL DEFAULT '',
  entry     TEXT NOT NULL,
  state     TEXT NOT NULL DEFAULT 'active',
  reason    TEXT NOT NULL DEFAULT '',
  added_by  TEXT NOT NULL DEFAULT '',
  added_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_egress_allow ON egress_allowlist(tenant_id, state);
CREATE TABLE IF NOT EXISTS deployment_setting(
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
