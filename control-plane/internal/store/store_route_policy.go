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
	// Writer is the key of the Control Plane that wrote the ceiling row (CeilingWriterKey); empty
	// for every other row and for a ceiling written before the column existed.
	Writer string
}

// RoutePolicyStore holds the policy rows.
type RoutePolicyStore interface {
	// GetRoutePolicy reads one row; tenantID is empty for the ceiling and deployment scopes.
	GetRoutePolicy(ctx context.Context, scope, tenantID string) (RoutePolicyRow, error)
	// GetRoutePolicyLayers reads, in one statement and so from one snapshot, every row a
	// tenant's members are judged by: the ceiling, the deployment rule, and the tenant's cap and
	// rule (tenantID empty reads the first two). Rows never written read as no restriction.
	GetRoutePolicyLayers(ctx context.Context, tenantID string) (RoutePolicyLayers, error)
	// ListTenantRoutePolicies reads every stored row of one tenant-scoped layer (RoutePolicyTenantCap
	// or RoutePolicyTenant), for a count across tenants. A tenant whose row was never written has
	// none; deleting a tenant deletes its rows.
	ListTenantRoutePolicies(ctx context.Context, scope string) ([]RoutePolicyRow, error)
	// PutRoutePolicy writes one row under a new revision, strictly greater than every revision
	// written before, and returns the row as it was before and as it is now. audit, when set,
	// builds the audit row from the two, inserted in the same transaction: the write and its
	// record commit together or not at all (D15).
	PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error)
	// PutRoutePolicyIf is PutRoutePolicy that writes only while the row is still at revision
	// *expect (0 = never written); a nil expect writes unconditionally. When the row has moved it
	// writes nothing, records nothing, takes no revision, and returns ErrRoutePolicyStale with
	// before set to the row as it is. The check and the write are one transaction.
	PutRoutePolicyIf(ctx context.Context, scope, tenantID, rule, actor string, expect *int64, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error)
	// PutCeiling writes the operator ceiling row on behalf of the Control Plane identified by
	// writer. When the row was written by a greater writer key (a Control Plane that started
	// later) it writes nothing, records nothing, takes no revision, and returns ErrCeilingSuperseded
	// with before set to the row as it is. The same writer key always passes, so a database edit
	// of the row is still re-recorded by the newest Control Plane.
	PutCeiling(ctx context.Context, rule, actor, writer string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error)
}

// ErrCeilingSuperseded: PutCeiling found the ceiling row written by a Control Plane that started later.
var ErrCeilingSuperseded = errors.New("route policy: the operator ceiling was recorded by a newer Control Plane")

// CeilingWriterKey orders Control Plane processes by start time; the random id breaks a tie
// between two that started in the same nanosecond. Fixed-width, so string order is time order.
func CeilingWriterKey(startNS int64, id string) string { return fmt.Sprintf("%020d-%s", startNS, id) }

// ErrRoutePolicyStale: a conditional PutRoutePolicyIf found the row at another revision than the
// caller loaded.
var ErrRoutePolicyStale = errors.New("route policy: the row changed after the revision the write was based on")

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
	err := q.QueryRowContext(ctx, `SELECT rule, revision, updated_by, updated_at, writer FROM route_policy WHERE scope=? AND tenant_id=?`,
		scope, tenantID).Scan(&r.Rule, &r.Revision, &r.UpdatedBy, &r.UpdatedAt, &r.Writer)
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

func (s *SQL) PutRoutePolicy(ctx context.Context, scope, tenantID, rule, actor string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error) {
	return s.PutRoutePolicyIf(ctx, scope, tenantID, rule, actor, nil, audit)
}

// PutRoutePolicyIf takes the next revision by updating route_policy_seq inside the write's
// transaction: on Postgres the update holds the row lock until commit, so two writes are
// serialised and commit in revision order, and a reader never sees a later revision committed
// before an earlier one. The same lock is what makes the precondition atomic: the row is read
// only after it is held, so a competing write has either committed (and is seen) or waits behind
// this one; two writes based on the same revision cannot both pass.
func (s *SQL) PutRoutePolicyIf(ctx context.Context, scope, tenantID, rule, actor string, expect *int64, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error) {
	return s.putRoutePolicy(ctx, scope, tenantID, rule, actor, "", expect, audit)
}

// PutCeiling: the supersede check runs after the sequence lock is held, so it reads the row as
// the last committed writer left it; a yield rolls back with the sequence untouched.
func (s *SQL) PutCeiling(ctx context.Context, rule, actor, writer string, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error) {
	return s.putRoutePolicy(ctx, RoutePolicyCeiling, "", rule, actor, writer, nil, audit)
}

// putRoutePolicy: a non-empty writer makes the write yield to a row written by a greater key.
func (s *SQL) putRoutePolicy(ctx context.Context, scope, tenantID, rule, actor, writer string, expect *int64, audit func(before, after RoutePolicyRow) AuditLog) (before, after RoutePolicyRow, err error) {
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
	if expect != nil && before.Revision != *expect {
		return before, RoutePolicyRow{}, ErrRoutePolicyStale
	}
	if writer != "" && before.Writer > writer {
		return before, RoutePolicyRow{}, ErrCeilingSuperseded
	}
	after = RoutePolicyRow{Scope: scope, TenantID: tenantID, Rule: rule, Revision: rev, UpdatedBy: actor, UpdatedAt: NowTS(), Writer: writer}
	if _, err = tx.ExecContext(ctx, `INSERT INTO route_policy(scope, tenant_id, rule, revision, updated_by, updated_at, writer)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, tenant_id) DO UPDATE SET rule=excluded.rule, revision=excluded.revision,
		updated_by=excluded.updated_by, updated_at=excluded.updated_at, writer=excluded.writer`,
		scope, tenantID, rule, rev, actor, after.UpdatedAt, writer); err != nil {
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

func (s *SQL) ListTenantRoutePolicies(ctx context.Context, scope string) ([]RoutePolicyRow, error) {
	if scope != RoutePolicyTenantCap && scope != RoutePolicyTenant {
		return nil, fmt.Errorf("route policy: %q is not a tenant-scoped layer", scope)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT scope, tenant_id, rule, revision, updated_by, updated_at FROM route_policy
		WHERE scope = ? AND tenant_id <> '' ORDER BY tenant_id`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoutePolicyRow
	for rows.Next() {
		var r RoutePolicyRow
		if err := rows.Scan(&r.Scope, &r.TenantID, &r.Rule, &r.Revision, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQL) GetRoutePolicyLayers(ctx context.Context, tenantID string) (RoutePolicyLayers, error) {
	blank := func(scope, tid string) RoutePolicyRow { return RoutePolicyRow{Scope: scope, TenantID: tid, Rule: "{}"} }
	out := RoutePolicyLayers{Ceiling: blank(RoutePolicyCeiling, ""), Deployment: blank(RoutePolicyDeployment, ""),
		TenantCap: blank(RoutePolicyTenantCap, tenantID), Tenant: blank(RoutePolicyTenant, tenantID)}
	rows, err := s.db.QueryContext(ctx, `SELECT scope, tenant_id, rule, revision, updated_by, updated_at, writer FROM route_policy
		WHERE (tenant_id = '' AND scope IN ('ceiling', 'deployment')) OR (tenant_id = ? AND tenant_id <> '' AND scope IN ('tenant_cap', 'tenant'))`,
		tenantID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RoutePolicyRow
		if err := rows.Scan(&r.Scope, &r.TenantID, &r.Rule, &r.Revision, &r.UpdatedBy, &r.UpdatedAt, &r.Writer); err != nil {
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
