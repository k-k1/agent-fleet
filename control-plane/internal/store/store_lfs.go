package store

import (
	"context"
	"database/sql"
	"strconv"
)

// LFSLock is one Git LFS file lock (docs/reference/internal-git-provider, P3).
// OwnerID is the locker's membership id; OwnerName is a display label.
type LFSLock struct {
	ID, TenantID, RepoName, Path, RefName, OwnerID, OwnerName, LockedAt string
}

// LFSObjectStore is the Git LFS object ledger (P3). PutLFSObject records an
// uploaded object (dedup on (tenant, repo, oid)); TenantLFSBytes sums a tenant's
// stored bytes for the capacity quota. Repo delete/rename move the ledger through
// GitRepoStore (the bytes on disk move with the .git dir).
type LFSObjectStore interface {
	// PutLFSObject records an upload. On a row that already exists it keeps the size and
	// moves created_at to now, so a retry of a failed publish restarts the grace period GC
	// gives a row whose file is absent (DeleteStaleLFSObject).
	PutLFSObject(ctx context.Context, tenantID, repo, oid string, size int64) error
	TenantLFSBytes(ctx context.Context, tenantID string) (int64, error)
	// DeleteLFSObject drops one object's ledger row (used by LFS GC when it prunes
	// an orphaned object from disk, so the tenant's capacity quota frees up).
	DeleteLFSObject(ctx context.Context, tenantID, repo, oid string) error
	// LFSObjectSize returns the size an existing ledger row records for one object.
	// A row can exist without its file (a failed publish keeps it), and a retry of that
	// upload must not be charged for the same bytes twice.
	LFSObjectSize(ctx context.Context, tenantID, repo, oid string) (int64, bool, error)
	// ListLFSObjects returns the ledger rows of a repo — the set GC reconciles against
	// the object files on disk.
	ListLFSObjects(ctx context.Context, tenantID, repo string) ([]LFSObject, error)
	// DeleteStaleLFSObject drops one row only if its created_at is at or before cutoff
	// (RFC 3339 UTC, as NowTS writes it), and reports whether it did. The age test sits in
	// the DELETE itself so an upload that refreshes the row after GC read it is not lost.
	DeleteStaleLFSObject(ctx context.Context, tenantID, repo, oid, cutoff string) (bool, error)
}

// LFSObject is one lfs_object ledger row.
type LFSObject struct {
	OID       string
	Size      int64
	CreatedAt string
}

// LFSLockStore is the Git LFS file-lock ledger (P3). CreateLFSLock inserts a
// lock (the (tenant, repo, path) UNIQUE makes a second lock on a path fail —
// the caller pre-checks for the 409). ListLFSLocks paginates by an opaque
// cursor (offset); a filter of "" matches all. Repo delete/rename move the locks
// through GitRepoStore.
type LFSLockStore interface {
	CreateLFSLock(ctx context.Context, l LFSLock) error
	GetLFSLockByPath(ctx context.Context, tenantID, repo, path string) (LFSLock, bool, error)
	GetLFSLock(ctx context.Context, tenantID, repo, id string) (LFSLock, bool, error)
	ListLFSLocks(ctx context.Context, tenantID, repo, filterPath, filterID string, limit int, cursor string) ([]LFSLock, string, error)
	DeleteLFSLock(ctx context.Context, tenantID, repo, id string) error
}

// --- Git LFS object ledger (docs/reference/internal-git-provider, P3) ---

func (s *SQL) PutLFSObject(ctx context.Context, tenantID, repo, oid string, size int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO lfs_object(tenant_id, repo_name, oid, size, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, repo_name, oid) DO UPDATE SET created_at=excluded.created_at`,
		tenantID, repo, oid, size, NowTS())
	return err
}

func (s *SQL) TenantLFSBytes(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size), 0) FROM lfs_object WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

func (s *SQL) DeleteLFSObject(ctx context.Context, tenantID, repo, oid string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_object WHERE tenant_id=? AND repo_name=? AND oid=?`, tenantID, repo, oid)
	return err
}

// --- Git LFS locks (docs/reference/internal-git-provider, P3) ---

const lfsLockCols = `SELECT id, tenant_id, repo_name, path, ref_name, owner_id, owner_name, locked_at FROM lfs_lock`

func scanLFSLock(row interface{ Scan(...any) error }) (LFSLock, error) {
	var l LFSLock
	err := row.Scan(&l.ID, &l.TenantID, &l.RepoName, &l.Path, &l.RefName, &l.OwnerID, &l.OwnerName, &l.LockedAt)
	return l, err
}

func (s *SQL) CreateLFSLock(ctx context.Context, l LFSLock) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO lfs_lock(id, tenant_id, repo_name, path, ref_name, owner_id, owner_name, locked_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.TenantID, l.RepoName, l.Path, l.RefName, l.OwnerID, l.OwnerName, l.LockedAt)
	return err
}

func (s *SQL) GetLFSLockByPath(ctx context.Context, tenantID, repo, path string) (LFSLock, bool, error) {
	l, err := scanLFSLock(s.db.QueryRowContext(ctx,
		lfsLockCols+` WHERE tenant_id=? AND repo_name=? AND path=?`, tenantID, repo, path))
	if err == sql.ErrNoRows {
		return LFSLock{}, false, nil
	}
	if err != nil {
		return LFSLock{}, false, err
	}
	return l, true, nil
}

func (s *SQL) GetLFSLock(ctx context.Context, tenantID, repo, id string) (LFSLock, bool, error) {
	l, err := scanLFSLock(s.db.QueryRowContext(ctx,
		lfsLockCols+` WHERE tenant_id=? AND repo_name=? AND id=?`, tenantID, repo, id))
	if err == sql.ErrNoRows {
		return LFSLock{}, false, nil
	}
	if err != nil {
		return LFSLock{}, false, err
	}
	return l, true, nil
}

// ListLFSLocks returns locks ordered oldest-first, paginated by an opaque cursor
// (a row offset). It fetches limit+1 to know whether a next page exists; the
// returned cursor is "" when the page is the last.
func (s *SQL) ListLFSLocks(ctx context.Context, tenantID, repo, filterPath, filterID string, limit int, cursor string) ([]LFSLock, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}
	q := lfsLockCols + ` WHERE tenant_id=? AND repo_name=?`
	args := []any{tenantID, repo}
	if filterPath != "" {
		q += ` AND path=?`
		args = append(args, filterPath)
	}
	if filterID != "" {
		q += ` AND id=?`
		args = append(args, filterID)
	}
	q += ` ORDER BY locked_at, id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []LFSLock
	for rows.Next() {
		l, err := scanLFSLock(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return out, next, nil
}

func (s *SQL) DeleteLFSLock(ctx context.Context, tenantID, repo, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_lock WHERE tenant_id=? AND repo_name=? AND id=?`, tenantID, repo, id)
	return err
}

func (s *SQL) LFSObjectSize(ctx context.Context, tenantID, repo, oid string) (int64, bool, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT size FROM lfs_object WHERE tenant_id=? AND repo_name=? AND oid=?`, tenantID, repo, oid).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return n, err == nil, err
}

func (s *SQL) ListLFSObjects(ctx context.Context, tenantID, repo string) ([]LFSObject, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT oid, size, created_at FROM lfs_object WHERE tenant_id=? AND repo_name=?`, tenantID, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LFSObject
	for rows.Next() {
		var o LFSObject
		if err := rows.Scan(&o.OID, &o.Size, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQL) DeleteStaleLFSObject(ctx context.Context, tenantID, repo, oid, cutoff string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_object WHERE tenant_id=? AND repo_name=? AND oid=? AND created_at<=?`,
		tenantID, repo, oid, cutoff)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
