package store

import (
	"context"
	"database/sql"
	"time"
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
	// ClearWorkspaceDisk withdraws the reading when the home is wiped (see the method).
	ClearWorkspaceDisk(ctx context.Context, workspaceID string) error
}

// DiskObservedAt formats a reading time the way observed_at is compared: UTC, fixed width, so
// string order is time order (an RFC 3339 second would let two readings in one second overwrite
// each other in either direction).
func DiskObservedAt(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// PutWorkspaceDisk stores d only for a workspace that still exists (there is no foreign key, and
// a sweep that outlived a Destroy would otherwise recreate a row nothing ever deletes) and only
// if it is not older than what is there, which includes a ClearWorkspaceDisk mark.
func (s *SQL) PutWorkspaceDisk(ctx context.Context, d WorkspaceDisk) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_disk(workspace_id, used, total, observed_at, cleared)
		 SELECT CAST(? AS TEXT), CAST(? AS BIGINT), CAST(? AS BIGINT), CAST(? AS TEXT), 0
		 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
		 ON CONFLICT(workspace_id) DO UPDATE SET used=excluded.used, total=excluded.total,
		   observed_at=excluded.observed_at, cleared=0
		 WHERE excluded.observed_at >= workspace_disk.observed_at`,
		d.WorkspaceID, int64(d.Used), int64(d.Total), d.ObservedAt, d.WorkspaceID)
	return err
}

// ClearWorkspaceDisk withdraws the reading after the home was wiped or erased: the old figure
// describes files that are gone. The row is kept as a mark stamped now, so a reading that began
// before the wipe and lands after it is refused rather than bringing the old figure back.
func (s *SQL) ClearWorkspaceDisk(ctx context.Context, workspaceID string) error {
	now := DiskObservedAt(time.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_disk(workspace_id, used, total, observed_at, cleared)
		 SELECT CAST(? AS TEXT), 0, 0, CAST(? AS TEXT), 1
		 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
		 ON CONFLICT(workspace_id) DO UPDATE SET used=0, total=0, observed_at=excluded.observed_at, cleared=1`,
		workspaceID, now, workspaceID)
	if err != nil {
		return err
	}
	// A wiped home starts over: the notices raised for the old one must be able to fire again.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM workspace_notice WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	// The last start's install outcome describes the wiped home too: the next start installs again.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workspace_boot_install(workspace_id, failed, observed_at)
		 SELECT CAST(? AS TEXT), '', CAST(? AS TEXT)
		 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
		 ON CONFLICT(workspace_id) DO UPDATE SET failed='', observed_at=excluded.observed_at`,
		workspaceID, now, workspaceID)
	return err
}

func (s *SQL) GetWorkspaceDisk(ctx context.Context, workspaceID string) (WorkspaceDisk, bool, error) {
	d := WorkspaceDisk{WorkspaceID: workspaceID}
	var used, total, cleared int64
	err := s.db.QueryRowContext(ctx,
		`SELECT used, total, observed_at, cleared FROM workspace_disk WHERE workspace_id=?`, workspaceID,
	).Scan(&used, &total, &d.ObservedAt, &cleared)
	if err == sql.ErrNoRows || (err == nil && cleared != 0) {
		return WorkspaceDisk{}, false, nil
	}
	if err != nil {
		return WorkspaceDisk{}, false, err
	}
	d.Used, d.Total = uint64(used), uint64(total)
	return d, true, nil
}
