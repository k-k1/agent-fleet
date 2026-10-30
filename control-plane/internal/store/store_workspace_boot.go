package store

import (
	"context"
	"database/sql"
)

// WorkspaceBootStore keeps the CLIs that failed to install on a workspace's last start. It is
// independent of the disk reading: an install can fail on a runtime whose disk is not measured
// (a working disk, a quota), and a disk that cannot be read must neither drop a new failure nor
// keep an old one.
type WorkspaceBootStore interface {
	// PutWorkspaceBootFailed stores the JSON object of tool to reason ("" = none failed) unless
	// an outcome observed later is already there.
	PutWorkspaceBootFailed(ctx context.Context, workspaceID, failed, observedAt string) error
	// GetWorkspaceBootFailed returns "" when none failed or nothing was observed.
	GetWorkspaceBootFailed(ctx context.Context, workspaceID string) (string, error)
}

// PutWorkspaceBootFailed stores only for a workspace that still exists, like PutWorkspaceDisk.
func (s *SQL) PutWorkspaceBootFailed(ctx context.Context, workspaceID, failed, observedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_boot_install(workspace_id, failed, observed_at)
		 SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT)
		 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
		 ON CONFLICT(workspace_id) DO UPDATE SET failed=excluded.failed, observed_at=excluded.observed_at
		 WHERE excluded.observed_at >= workspace_boot_install.observed_at`,
		workspaceID, failed, observedAt, workspaceID)
	return err
}

func (s *SQL) GetWorkspaceBootFailed(ctx context.Context, workspaceID string) (string, error) {
	var failed string
	err := s.db.QueryRowContext(ctx,
		`SELECT failed FROM workspace_boot_install WHERE workspace_id=?`, workspaceID).Scan(&failed)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return failed, err
}
