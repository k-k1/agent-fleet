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
	// GetRoutePolicy reads one row; tenantID is empty for the deployment scope.
	GetRoutePolicy(ctx context.Context, scope, tenantID string) (RoutePolicyRow, error)
	// PutRoutePolicy writes one row under a new revision, strictly greater than every revision
	// written before, and returns the row as it was before and as it is now.
	PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string) (before, after RoutePolicyRow, err error)
}

func validRoutePolicyScope(scope, tenantID string) error {
	switch scope {
	case RoutePolicyDeployment:
		if tenantID != "" {
			return fmt.Errorf("the deployment rule has no tenant")
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
func (s *SQL) PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string) (before, after RoutePolicyRow, err error) {
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
	return before, after, tx.Commit()
}
