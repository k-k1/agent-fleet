-- Git LFS object ledger for the internal git provider (docs/reference/
-- internal-git-provider, P3). The actual bytes live content-addressed under
-- ${DATA_DIR}/git/<slug>/<repo>.git/lfs/objects/ and this table is the
-- accounting ledger, so the per-tenant capacity quota (max_lfs_bytes) is an O(1)
-- SUM instead of an FS walk. Keyed by (tenant, repo, oid) so a re-push of the
-- same object dedups. Rows follow their repo (deleted on repo delete, repo_name
-- updated on rename) while the on-disk objects move with the .git dir.
CREATE TABLE lfs_object (
  tenant_id  TEXT    NOT NULL REFERENCES tenant(id),
  repo_name  TEXT    NOT NULL,
  oid        TEXT    NOT NULL,          -- lowercase hex sha256
  size       INTEGER NOT NULL,
  created_at TEXT    NOT NULL,
  PRIMARY KEY (tenant_id, repo_name, oid)
);
CREATE INDEX idx_lfs_object_tenant ON lfs_object(tenant_id);
