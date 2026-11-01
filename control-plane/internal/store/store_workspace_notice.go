package store

import "context"

// Kinds of admin notice a workspace can have raised (#2427).
const (
	WorkspaceNoticeHomeFull    = "home_full"
	WorkspaceNoticeBootInstall = "boot_install"
)

// WorkspaceNoticeStore keeps the "already notified" mark of each admin notice, one row per
// (workspace, kind) while the condition holds.
type WorkspaceNoticeStore interface {
	// WorkspaceNoticeRaised reports whether the notice is currently marked raised.
	WorkspaceNoticeRaised(ctx context.Context, workspaceID, kind string) (bool, error)
	// ClaimWorkspaceNotice marks the notice raised and reports true only to the one caller whose
	// write created the mark; a concurrent sweep or another replica gets false and stays silent.
	// A workspace that no longer exists is never claimed.
	ClaimWorkspaceNotice(ctx context.Context, workspaceID, kind string) (bool, error)
	// ReleaseWorkspaceNotice withdraws the mark so a later crossing notifies again, and reports
	// whether there was one.
	ReleaseWorkspaceNotice(ctx context.Context, workspaceID, kind string) (bool, error)
}

func (s *SQL) WorkspaceNoticeRaised(ctx context.Context, workspaceID, kind string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_notice WHERE workspace_id=? AND kind=?`, workspaceID, kind).Scan(&n)
	return n > 0, err
}

// ClaimWorkspaceNotice is one INSERT guarded by the primary key of the target row (ON CONFLICT DO
// NOTHING), so exactly one concurrent caller sees RowsAffected 1 on both SQLite and Postgres. The
// EXISTS looks at the workspace table, never at workspace_notice itself.
func (s *SQL) ClaimWorkspaceNotice(ctx context.Context, workspaceID, kind string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_notice(workspace_id, kind, raised_at)
		 SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT)
		 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
		 ON CONFLICT(workspace_id, kind) DO NOTHING`,
		workspaceID, kind, NowTS(), workspaceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQL) ReleaseWorkspaceNotice(ctx context.Context, workspaceID, kind string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_notice WHERE workspace_id=? AND kind=?`, workspaceID, kind)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
