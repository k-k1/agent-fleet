package store

import (
	"context"
)

// AuditLog records one administrative action (docs/decisions/0006, P3-6).
// ActorKind ∈ user | admin | mcp | system; ActorID is the identity/PAT id behind
// it. TenantID scopes the entry ("" = deployment-wide). HTTPStatus is the
// proxied operation's status when recorded (0 for legacy/non-HTTP events).
// At is RFC3339.
type AuditLog struct {
	ID, TenantID, ActorKind, ActorID, Action, Target, Detail, At string
	HTTPStatus                                                   int
}

// AuditStore is the audit log (docs/decisions/0006, P3-6; docs/log/20 M1).
// InsertAudit records one action; ListAuditByTenant serves the most recent
// entries (newest first) scoped to a tenant, or — when tenantID=="" —
// deployment-wide (super_admin).
type AuditStore interface {
	InsertAudit(ctx context.Context, a AuditLog) error
	ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error)
}

func (s *SQL) InsertAudit(ctx context.Context, a AuditLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TenantID, a.ActorKind, a.ActorID, a.Action, a.Target, a.Detail, a.At, a.HTTPStatus)
	return err
}

func (s *SQL) ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	// tenantID=="" spans every tenant (super_admin, deployment-wide view); a set
	// tenantID scopes to that tenant only.
	q := `SELECT id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status FROM audit_log`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ActorKind, &a.ActorID,
			&a.Action, &a.Target, &a.Detail, &a.At, &a.HTTPStatus); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
