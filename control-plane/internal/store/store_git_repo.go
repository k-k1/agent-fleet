package store

import (
	"context"
	"database/sql"
)

// GitRepo is one internal bare repository owned by a tenant (docs/reference/
// internal-git-provider). The on-disk bare lives at ${DATA_DIR}/git/<slug>/<name>.git;
// this row is the ledger the list/serve paths trust over an FS walk.
type GitRepo struct {
	ID, TenantID, Name, DefaultBranch, CreatedBy, CreatedAt string
}

// GitRepoStore is the internal-git repository ledger (docs/reference/
// internal-git-provider, ADR 0010). CreateGitRepo inserts the ledger row (the
// bare is created on disk by the caller); the (tenant_id, name) uniqueness is
// enforced by the schema. RenameGitRepo and DeleteGitRepo carry the repo's LFS
// ledger and lock rows with them in the same transaction.
type GitRepoStore interface {
	CreateGitRepo(ctx context.Context, g GitRepo) error
	ListGitReposByTenant(ctx context.Context, tenantID string) ([]GitRepo, error)
	GetGitRepo(ctx context.Context, tenantID, name string) (GitRepo, bool, error)
	CountGitReposByTenant(ctx context.Context, tenantID string) (int, error)
	RenameGitRepo(ctx context.Context, tenantID, oldName, newName string) error
	DeleteGitRepo(ctx context.Context, tenantID, name string) error
}

// --- Internal git repositories (docs/reference/internal-git-provider) ---

func (s *SQL) CreateGitRepo(ctx context.Context, g GitRepo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO git_repo(id, tenant_id, name, default_branch, created_by, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		g.ID, g.TenantID, g.Name, g.DefaultBranch, nullable(g.CreatedBy), g.CreatedAt)
	return err
}

func (s *SQL) ListGitReposByTenant(ctx context.Context, tenantID string) ([]GitRepo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, default_branch, COALESCE(created_by,''), created_at
		 FROM git_repo WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitRepo
	for rows.Next() {
		var g GitRepo
		if err := rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.DefaultBranch, &g.CreatedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *SQL) GetGitRepo(ctx context.Context, tenantID, name string) (GitRepo, bool, error) {
	var g GitRepo
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, default_branch, COALESCE(created_by,''), created_at
		 FROM git_repo WHERE tenant_id=? AND name=?`, tenantID, name).
		Scan(&g.ID, &g.TenantID, &g.Name, &g.DefaultBranch, &g.CreatedBy, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return GitRepo{}, false, nil
	}
	if err != nil {
		return GitRepo{}, false, err
	}
	return g, true, nil
}

func (s *SQL) CountGitReposByTenant(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM git_repo WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

// gitRepoTables are the tables keyed by (tenant_id, repo name) that follow a repo
// through rename and delete. Changing them together is what keeps the LFS quota and
// the locks from drifting away from the repo on disk.
var gitRepoTables = []struct{ table, col string }{
	{"git_repo", "name"},
	{"lfs_object", "repo_name"},
	{"lfs_lock", "repo_name"},
}

// RenameGitRepo renames one repo within a tenant, together with its LFS ledger and
// lock rows, in one transaction. The (tenant_id, name) UNIQUE constraint makes a
// collision with an existing name an error (surfaced as a 409 by the caller after a
// pre-check).
func (s *SQL) RenameGitRepo(ctx context.Context, tenantID, oldName, newName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range gitRepoTables {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+t.table+` SET `+t.col+`=? WHERE tenant_id=? AND `+t.col+`=?`,
			newName, tenantID, oldName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteGitRepo deletes one repo's row with its LFS ledger and lock rows, in one
// transaction: a stale ledger over-counts the tenant's LFS quota, and stale locks
// would be inherited by a later repo of the same name.
func (s *SQL) DeleteGitRepo(ctx context.Context, tenantID, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range gitRepoTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+t.table+` WHERE tenant_id=? AND `+t.col+`=?`, tenantID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
