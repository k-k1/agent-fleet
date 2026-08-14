-- Tenant-defined login providers (docs/61 §61.11 + ADR0043 決定 29-33), Postgres
-- mirror of migrations/0040_tenant_idp.sql. See that file for the column semantics
-- and for why a row is born pending. The DDL is dialect-neutral (TEXT only), so the
-- two files stay identical apart from the migration series.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS tenant_idp(
  id              TEXT PRIMARY KEY,
  tenant_id       TEXT NOT NULL,
  name            TEXT NOT NULL,
  label_ja        TEXT NOT NULL DEFAULT '',
  label_en        TEXT NOT NULL DEFAULT '',
  issuer          TEXT NOT NULL,
  client_id       TEXT NOT NULL,
  secret_enc      TEXT NOT NULL DEFAULT '',
  key_ref         TEXT NOT NULL DEFAULT '',
  trust           TEXT NOT NULL,
  allowed_tids    TEXT NOT NULL DEFAULT '',
  allowed_domains TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'pending',
  approved_by     TEXT NOT NULL DEFAULT '',
  approved_at     TEXT NOT NULL DEFAULT '',
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_idp_name ON tenant_idp(tenant_id, name)
