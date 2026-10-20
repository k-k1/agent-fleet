package store

import (
	"context"
	"database/sql"
)

// WorkspaceDisk is the last home-disk reading the usage sampler took for a workspace.
// Total is 0 when the reading has no denominator (a local or native deployment with no disk
// quota set): the figure is then "used" alone and no gauge can be drawn from it.
type WorkspaceDisk struct {
	WorkspaceID string
	Used, Total uint64
	// ObservedAt is when the reading was TAKEN (RFC 3339, UTC), not when it was stored: the
	// roster shows its age on a stopped card, and a stopped workspace is not measured again.
	ObservedAt string
}

// WorkspaceDiskStore keeps one row per workspace, so the member roster can show a disk gauge
// from the DB alone, including while the workspace is stopped.
type WorkspaceDiskStore interface {
	// PutWorkspaceDisk stores d unless a reading taken later is already there, so a sweep
	// that was slow cannot roll a newer observation back.
	PutWorkspaceDisk(ctx context.Context, d WorkspaceDisk) error
	// GetWorkspaceDisk returns ok=false when the workspace was never measured.
	GetWorkspaceDisk(ctx context.Context, workspaceID string) (WorkspaceDisk, bool, error)
}

func (s *SQL) PutWorkspaceDisk(ctx context.Context, d WorkspaceDisk) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_disk(workspace_id, used, total, observed_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET used=excluded.used, total=excluded.total,
		   observed_at=excluded.observed_at
		 WHERE excluded.observed_at >= workspace_disk.observed_at`,
		d.WorkspaceID, int64(d.Used), int64(d.Total), d.ObservedAt)
	return err
}

func (s *SQL) GetWorkspaceDisk(ctx context.Context, workspaceID string) (WorkspaceDisk, bool, error) {
	d := WorkspaceDisk{WorkspaceID: workspaceID}
	var used, total int64
	err := s.db.QueryRowContext(ctx,
		`SELECT used, total, observed_at FROM workspace_disk WHERE workspace_id=?`, workspaceID,
	).Scan(&used, &total, &d.ObservedAt)
	if err == sql.ErrNoRows {
		return WorkspaceDisk{}, false, nil
	}
	if err != nil {
		return WorkspaceDisk{}, false, err
	}
	d.Used, d.Total = uint64(used), uint64(total)
	return d, true, nil
}
