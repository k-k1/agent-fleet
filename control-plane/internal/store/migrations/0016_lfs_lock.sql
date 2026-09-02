-- Git LFS file locks for the internal git provider (docs/reference/
-- internal-git-provider, P3). The LFS locking API lets a member reserve a
-- (usually binary) path so teammates see it is being edited. A path can hold at
-- most one lock per (tenant, repo). Locks follow their repo: deleted on repo
-- delete, repo_name updated on rename. owner_id is the locker's membership id
-- (stable, used for ours/theirs on verify) and owner_name is a display label.
CREATE TABLE lfs_lock (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL REFERENCES tenant(id),
  repo_name  TEXT NOT NULL,
  path       TEXT NOT NULL,
  ref_name   TEXT NOT NULL DEFAULT '',
  owner_id   TEXT NOT NULL,
  owner_name TEXT NOT NULL DEFAULT '',
  locked_at  TEXT NOT NULL,
  UNIQUE(tenant_id, repo_name, path)
);
CREATE INDEX idx_lfs_lock_repo ON lfs_lock(tenant_id, repo_name);
