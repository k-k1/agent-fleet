package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Credential policy rows (ADR 0138 D7, D8): the super_admin's deployment rule and per-tenant cap
// and the tenant_admin's rule, each its own row with its own revision. The rule is stored as the
// JSON of routepolicy.Rule; this layer neither parses nor judges it.

// Route policy scopes.
const (
	// RoutePolicyCeiling is the operator ceiling as a Control Plane last recorded it, so that a
	// change of it takes a revision. Each CP's environment, not this row, is what it enforces
	// (D9c); a CP that finds the row differing from its own re-records it before answering.
	RoutePolicyCeiling    = "ceiling"
	RoutePolicyDeployment = "deployment"
	RoutePolicyTenantCap  = "tenant_cap"
	RoutePolicyTenant     = "tenant"
)

// RoutePolicyRow is one stored layer. A row never written reads as Rule "{}" and Revision 0.
type RoutePolicyRow struct {
	Scope     string
	TenantID  string
	Rule      string
	Revision  int64
	UpdatedBy string
	UpdatedAt string
}

// RoutePolicyStore holds the policy rows.
type RoutePolicyStore interface {
	// GetRoutePolicy reads one row; tenantID is empty for the ceiling and deployment scopes.
	GetRoutePolicy(ctx context.Context, scope, tenantID string) (RoutePolicyRow, error)
	// GetRoutePolicyLayers reads, in one statement and so from one snapshot, every row a
	// tenant's members are judged by: the ceiling, the deployment rule, and the tenant's cap and
	// rule (tenantID empty reads the first two). Rows never written read as no restriction.
	GetRoutePolicyLayers(ctx context.Context, tenantID string) (RoutePolicyLayers, error)
	// PutRoutePolicy writes one row under a new revision, strictly greater than every revision
	// written before, and returns the row as it was before and as it is now. audit, when set,
	// builds the audit row from the two, inserted in the same transaction: the write and its
	// record commit together or not at all (D15).
	PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error)
}

// RoutePolicyLayers is one consistent read of a tenant's layers.
type RoutePolicyLayers struct {
	Ceiling, Deployment, TenantCap, Tenant RoutePolicyRow
}

// Revision is the effective revision of the read: the newest of its rows.
func (l RoutePolicyLayers) Revision() int64 {
	return max(l.Ceiling.Revision, l.Deployment.Revision, l.TenantCap.Revision, l.Tenant.Revision)
}

func validRoutePolicyScope(scope, tenantID string) error {
	switch scope {
	case RoutePolicyCeiling, RoutePolicyDeployment:
		if tenantID != "" {
			return fmt.Errorf("the %s row has no tenant", scope)
		}
	case RoutePolicyTenantCap, RoutePolicyTenant:
		if tenantID == "" {
			return fmt.Errorf("a %s row needs a tenant", scope)
		}
	default:
		return fmt.Errorf("unknown route policy scope %q", scope)
	}
	return nil
}

func getRoutePolicy(ctx context.Context, q rowQuerier, scope, tenantID string) (RoutePolicyRow, error) {
	r := RoutePolicyRow{Scope: scope, TenantID: tenantID, Rule: "{}"}
	err := q.QueryRowContext(ctx, `SELECT rule, revision, updated_by, updated_at FROM route_policy WHERE scope=? AND tenant_id=?`,
		scope, tenantID).Scan(&r.Rule, &r.Revision, &r.UpdatedBy, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	return r, err
}

func (s *SQL) GetRoutePolicy(ctx context.Context, scope, tenantID string) (RoutePolicyRow, error) {
	if err := validRoutePolicyScope(scope, tenantID); err != nil {
		return RoutePolicyRow{}, err
	}
	return getRoutePolicy(ctx, s.db, scope, tenantID)
}

// PutRoutePolicy takes the next revision by updating route_policy_seq inside the write's
// transaction: on Postgres the update holds the row lock until commit, so two writes are
// serialised and commit in revision order, and a reader never sees a later revision committed
// before an earlier one.
func (s *SQL) PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error) {
	if err := validRoutePolicyScope(scope, tenantID); err != nil {
		return before, after, err
	}
	if rule == "" {
		rule = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return before, after, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE route_policy_seq SET n = n + 1 WHERE id = 1`); err != nil {
		return before, after, err
	}
	var rev int64
	if err = tx.QueryRowContext(ctx, `SELECT n FROM route_policy_seq WHERE id = 1`).Scan(&rev); err != nil {
		return before, after, err
	}
	if before, err = getRoutePolicy(ctx, tx, scope, tenantID); err != nil {
		return before, after, err
	}
	after = RoutePolicyRow{Scope: scope, TenantID: tenantID, Rule: rule, Revision: rev, UpdatedBy: actor, UpdatedAt: NowTS()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO route_policy(scope, tenant_id, rule, revision, updated_by, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, tenant_id) DO UPDATE SET rule=excluded.rule, revision=excluded.revision,
		updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		scope, tenantID, rule, rev, actor, after.UpdatedAt); err != nil {
		return before, after, err
	}
	if audit != nil {
		a := audit(before, after)
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.TenantID, a.ActorKind, a.ActorID, a.Action, a.Target, a.Detail, a.At, a.HTTPStatus); err != nil {
			return before, after, fmt.Errorf("recording the audit row: %w", err)
		}
	}
	return before, after, tx.Commit()
}

func (s *SQL) GetRoutePolicyLayers(ctx context.Context, tenantID string) (RoutePolicyLayers, error) {
	blank := func(scope, tid string) RoutePolicyRow { return RoutePolicyRow{Scope: scope, TenantID: tid, Rule: "{}"} }
	out := RoutePolicyLayers{Ceiling: blank(RoutePolicyCeiling, ""), Deployment: blank(RoutePolicyDeployment, ""),
		TenantCap: blank(RoutePolicyTenantCap, tenantID), Tenant: blank(RoutePolicyTenant, tenantID)}
	rows, err := s.db.QueryContext(ctx, `SELECT scope, tenant_id, rule, revision, updated_by, updated_at FROM route_policy
		WHERE (tenant_id = '' AND scope IN ('ceiling', 'deployment')) OR (tenant_id = ? AND tenant_id <> '' AND scope IN ('tenant_cap', 'tenant'))`,
		tenantID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RoutePolicyRow
		if err := rows.Scan(&r.Scope, &r.TenantID, &r.Rule, &r.Revision, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return out, err
		}
		switch r.Scope {
		case RoutePolicyCeiling:
			out.Ceiling = r
		case RoutePolicyDeployment:
			out.Deployment = r
		case RoutePolicyTenantCap:
			out.TenantCap = r
		case RoutePolicyTenant:
			out.Tenant = r
		}
	}
	return out, rows.Err()
}
