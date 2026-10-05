package store

import (
	"context"
	"database/sql"
)

// TenantBranchRules is a tenant's branch naming rules (ADR 0103 decision 10). Rules is the
// JSON list exactly as validated and saved by the admin API; the store does not parse it.
type TenantBranchRules struct {
	TenantID  string
	Rules     string
	UpdatedBy string
	UpdatedAt string
}

// TenantBranchRulesStore holds the tenant layer of the branch naming resolver. Every call
// carries tenant_id, so one tenant's admin can never read or write another's rules.
type TenantBranchRulesStore interface {
	// GetTenantBranchRules returns ok=false when the tenant never saved any.
	GetTenantBranchRules(ctx context.Context, tenantID string) (TenantBranchRules, bool, error)
	// PutTenantBranchRules replaces the tenant's whole list.
	PutTenantBranchRules(ctx context.Context, row TenantBranchRules) error
}

func (s *SQL) GetTenantBranchRules(ctx context.Context, tenantID string) (TenantBranchRules, bool, error) {
	r := TenantBranchRules{TenantID: tenantID}
	err := s.db.QueryRowContext(ctx,
		`SELECT rules, updated_by, updated_at FROM tenant_branch_rules WHERE tenant_id=?`, tenantID,
	).Scan(&r.Rules, &r.UpdatedBy, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return TenantBranchRules{TenantID: tenantID}, false, nil
	}
	return r, err == nil, err
}

func (s *SQL) PutTenantBranchRules(ctx context.Context, r TenantBranchRules) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_branch_rules(tenant_id, rules, updated_by, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET
		   rules=excluded.rules, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		r.TenantID, r.Rules, r.UpdatedBy, r.UpdatedAt)
	return err
}
