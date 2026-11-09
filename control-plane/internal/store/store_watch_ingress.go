package store

import (
	"context"
	"database/sql"
	"errors"
)

// WatchTarget is a running workspace that registered a subject.
type WatchTarget struct {
	MembershipID string
	WorkspaceID  string
}

// WatchHook is one webhook endpoint a tenant owns (ADR 0133 §4). The secret is derived from the
// hook id by the Control Plane and is not part of the row.
type WatchHook struct {
	ID, TenantID, MembershipID, Source, Repo, CreatedAt string
}

// ErrWatchLimit is returned when a bound on registrations or hooks is reached.
var ErrWatchLimit = errors.New("watch ingress limit reached")

// WatchIngressStore holds what the webhook ingress needs: which workspace asked to be poked
// about which subject, and the hooks a tenant owns. It never holds a payload.
type WatchIngressStore interface {
	// ReplaceWatchSubjects makes subjects the whole set of membership's registrations until
	// expiresAt. More than max subjects is ErrWatchLimit and changes nothing.
	ReplaceWatchSubjects(ctx context.Context, membershipID, tenantID string, subjects []WatchSubjectRow, expiresAt string, max int) error
	// WatchTargets returns up to limit running workspaces that hold an unexpired registration
	// of subject. tenantID "" matches every tenant, any other value only that tenant.
	WatchTargets(ctx context.Context, subject, tenantID, now string, limit int) ([]WatchTarget, error)
	// SweepWatchSubjects deletes registrations that expired before now.
	SweepWatchSubjects(ctx context.Context, now string) error

	// CreateWatchHook stores h unless its membership already owns max hooks (ErrWatchLimit).
	CreateWatchHook(ctx context.Context, h WatchHook, max int) error
	GetWatchHook(ctx context.Context, id string) (WatchHook, bool, error)
	ListWatchHooks(ctx context.Context, membershipID string) ([]WatchHook, error)
	// DeleteWatchHook removes the membership's own hook, reporting whether there was one.
	DeleteWatchHook(ctx context.Context, membershipID, id string) (bool, error)
}

// WatchSubjectRow is one registration.
type WatchSubjectRow struct{ Source, Subject string }

func (s *SQL) ReplaceWatchSubjects(ctx context.Context, membershipID, tenantID string, subjects []WatchSubjectRow, expiresAt string, max int) error {
	if len(subjects) > max {
		return ErrWatchLimit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM watch_subject WHERE membership_id=?`, membershipID); err != nil {
		return err
	}
	for _, r := range subjects {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO watch_subject(membership_id, tenant_id, source, subject, expires_at) VALUES(?,?,?,?,?)
			 ON CONFLICT(membership_id, subject) DO UPDATE SET expires_at=excluded.expires_at`,
			membershipID, tenantID, r.Source, r.Subject, expiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQL) WatchTargets(ctx context.Context, subject, tenantID, now string, limit int) ([]WatchTarget, error) {
	q := `SELECT w.membership_id, w.id FROM watch_subject s JOIN workspace w ON w.membership_id = s.membership_id
	      WHERE s.subject=? AND s.expires_at>? AND w.state='running'`
	args := []any{subject, now}
	if tenantID != "" {
		q += ` AND s.tenant_id=? AND w.tenant_id=?`
		args = append(args, tenantID, tenantID)
	}
	q += ` ORDER BY w.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchTarget
	for rows.Next() {
		var t WatchTarget
		if err := rows.Scan(&t.MembershipID, &t.WorkspaceID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQL) SweepWatchSubjects(ctx context.Context, now string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM watch_subject WHERE expires_at<=?`, now)
	return err
}

func (s *SQL) CreateWatchHook(ctx context.Context, h WatchHook, max int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO watch_hook(id, tenant_id, membership_id, source, repo, created_at) VALUES(?,?,?,?,?,?)`,
		h.ID, h.TenantID, h.MembershipID, h.Source, h.Repo, h.CreatedAt); err != nil {
		return err
	}
	// A soft cap: two concurrent creates that both start below the limit may both commit, so
	// the limit can be exceeded by a row or two. It bounds growth, it is not an invariant.
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_hook WHERE membership_id=?`, h.MembershipID).Scan(&n); err != nil {
		return err
	}
	if n > max {
		return ErrWatchLimit
	}
	return tx.Commit()
}

func (s *SQL) GetWatchHook(ctx context.Context, id string) (WatchHook, bool, error) {
	var h WatchHook
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, membership_id, source, repo, created_at FROM watch_hook WHERE id=?`, id,
	).Scan(&h.ID, &h.TenantID, &h.MembershipID, &h.Source, &h.Repo, &h.CreatedAt)
	if err == sql.ErrNoRows {
		return WatchHook{}, false, nil
	}
	return h, err == nil, err
}

func (s *SQL) ListWatchHooks(ctx context.Context, membershipID string) ([]WatchHook, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, membership_id, source, repo, created_at FROM watch_hook WHERE membership_id=? ORDER BY created_at, id`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchHook
	for rows.Next() {
		var h WatchHook
		if err := rows.Scan(&h.ID, &h.TenantID, &h.MembershipID, &h.Source, &h.Repo, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQL) DeleteWatchHook(ctx context.Context, membershipID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM watch_hook WHERE id=? AND membership_id=?`, id, membershipID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
