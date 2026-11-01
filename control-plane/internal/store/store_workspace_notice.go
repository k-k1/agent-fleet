package store

import (
	"context"
	"database/sql"
	"errors"
)

// Kinds of admin notice a workspace can have raised (#2427).
const (
	WorkspaceNoticeHomeFull    = "home_full"
	WorkspaceNoticeBootInstall = "boot_install"
)

// NoticeReading is what a notice decision reads: the stored home reading and the stored install
// outcome of the workspace, both as of the moment the evaluation holds the source row.
type NoticeReading struct {
	// Disk is the stored home reading; DiskOK is false when none exists or it was cleared.
	Disk       WorkspaceDisk
	DiskOK     bool
	BootFailed string
}

// NoticeDecision is the outcome of one evaluation. Notes builds the notifications to write when
// the notice is newly raised, from the tenant's active roster read in the same transaction.
type NoticeDecision struct {
	Want  bool
	Notes func(members []MemberInfo) []Notification
}

// WorkspaceNoticeStore keeps the "already notified" mark of each admin notice, one row per
// (workspace, kind) while the condition holds.
type WorkspaceNoticeStore interface {
	// WorkspaceNoticeRaised reports whether the notice is currently marked raised.
	WorkspaceNoticeRaised(ctx context.Context, workspaceID, kind string) (bool, error)
	// SyncWorkspaceNotice evaluates one notice atomically. In ONE transaction it locks the source
	// row (the stored reading the kind is decided from), reads that reading and the mark, calls
	// decide, and then either creates the mark and writes every notification, or deletes the
	// mark. A failure anywhere (a cancelled context, a crash, one recipient's insert) rolls the
	// mark back with the notifications, so the next sample retries from nothing; a concurrent
	// evaluation or a writer of the source row waits for the lock and sees the result.
	SyncWorkspaceNotice(ctx context.Context, ws Workspace, kind string,
		decide func(r NoticeReading, prev bool) NoticeDecision) error
}

func (s *SQL) WorkspaceNoticeRaised(ctx context.Context, workspaceID, kind string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_notice WHERE workspace_id=? AND kind=?`, workspaceID, kind).Scan(&n)
	return n > 0, err
}

func (s *SQL) SyncWorkspaceNotice(ctx context.Context, ws Workspace, kind string,
	decide func(r NoticeReading, prev bool) NoticeDecision) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// FOR UPDATE on the source row makes a writer of that row (the sampler's Put) wait for this
	// evaluation, and a second evaluation wait for the first, so the reading, the mark and the
	// notifications are one consistent step. SQLite has one connection and one writer, so the
	// transaction alone serialises there.
	lock := ""
	if s.dialect == "postgres" {
		lock = " FOR UPDATE"
	}
	var r NoticeReading
	switch kind {
	case WorkspaceNoticeHomeFull:
		var used, total, cleared int64
		err = tx.QueryRowContext(ctx,
			`SELECT used, total, observed_at, cleared FROM workspace_disk WHERE workspace_id=?`+lock, ws.ID,
		).Scan(&used, &total, &r.Disk.ObservedAt, &cleared)
		if err == nil && cleared == 0 {
			r.Disk.WorkspaceID, r.Disk.Used, r.Disk.Total, r.DiskOK = ws.ID, uint64(used), uint64(total), true
		}
	case WorkspaceNoticeBootInstall:
		err = tx.QueryRowContext(ctx,
			`SELECT failed FROM workspace_boot_install WHERE workspace_id=?`+lock, ws.ID).Scan(&r.BootFailed)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_notice WHERE workspace_id=? AND kind=?`, ws.ID, kind).Scan(&n); err != nil {
		return err
	}
	prev := n > 0
	d := decide(r, prev)
	switch {
	case d.Want && !prev:
		// The PRIMARY KEY on the target row is the guard; the EXISTS reads the workspace table.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO workspace_notice(workspace_id, kind, raised_at)
			 SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT)
			 WHERE EXISTS (SELECT 1 FROM workspace WHERE id=?)
			 ON CONFLICT(workspace_id, kind) DO NOTHING`, ws.ID, kind, NowTS(), ws.ID)
		if err != nil {
			return err
		}
		if c, err := res.RowsAffected(); err != nil || c != 1 {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT m.id, i.user_key, i.email, i.role, m.role
			 FROM membership m JOIN identity i ON i.id = m.identity_id
			 WHERE m.tenant_id=? AND m.status='active' ORDER BY i.user_key`, ws.TenantID)
		if err != nil {
			return err
		}
		var members []MemberInfo
		for rows.Next() {
			var mi MemberInfo
			if err := rows.Scan(&mi.MembershipID, &mi.UserKey, &mi.Email, &mi.IdentityRole, &mi.MemberRole); err != nil {
				rows.Close()
				return err
			}
			members = append(members, mi)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var notes []Notification
		if d.Notes != nil {
			notes = d.Notes(members)
		}
		// No recipient: leave no mark, so the notice fires once someone can receive it.
		if len(notes) == 0 {
			return nil
		}
		for _, nt := range notes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notification(event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at)
				VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`, nt.EventID, nt.MembershipID,
				nt.Kind, nt.TargetType, nt.TargetID, nt.TargetKind, nt.DisplayName, nt.Payload, nt.CreatedAt, nt.SeenAt); err != nil {
				return err
			}
		}
	case !d.Want && prev:
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM workspace_notice WHERE workspace_id=? AND kind=?`, ws.ID, kind); err != nil {
			return err
		}
	default:
		return nil
	}
	return tx.Commit()
}
