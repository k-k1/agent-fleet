-- Internal git provider (docs/reference/internal-git-provider, ADR 0010).
-- The CP self-hosts one bare repo per (tenant, name) under
-- ${DATA_DIR}/git/<tenant-slug>/<name>.git and serves it over smart-HTTP.
-- This table is the ledger (source of truth over FS walks): it drives the repo
-- list and gates which repos the smart-HTTP handler will serve. The git access
-- token is NOT stored here — it is a deterministic per-membership HMAC minted on
-- the fly (see git_http.go), so there is no token table to reuse or manage.
CREATE TABLE git_repo (
  id             TEXT PRIMARY KEY,
  tenant_id      TEXT NOT NULL REFERENCES tenant(id),
  name           TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  created_by     TEXT,               -- membership id of the creator (audit)
  created_at     TEXT NOT NULL,
  UNIQUE(tenant_id, name)
);
CREATE INDEX idx_git_repo_tenant ON git_repo(tenant_id);
