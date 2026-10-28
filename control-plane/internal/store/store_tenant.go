package store

import (
	"context"
	"database/sql"
	"fmt"
)

type Tenant struct {
	ID, Slug, Name, Status, Limits, Isolation, KeyRef, CreatedAt string
	// Per-tenant login rules (docs/log/61 §61.9.7, migration 0039). All CSV, all
	// optional; see TenantLoginRules for what each one governs and why there is no
	// allowed_emails among them. HiddenProviders (0042) is the one that is DISPLAY
	// only — accepted methods to leave off this tenant's login page (§61.15.9).
	AllowedProviders, AutoJoinDomains, AllowedDomains, HiddenProviders string
}

// TenantLoginRules is the login-relevant slice of a tenant, as loaded in bulk by
// the entry gate's cache (docs/log/61 §61.9.6/§61.9.7). Slug is carried because the
// deterministic tie-break for two tenants claiming the same auto-join domain is
// "lowest slug wins" (§61.9.8).
type TenantLoginRules struct {
	ID, Slug, Name                                    string
	AllowedProviders, AutoJoinDomains, AllowedDomains []string
	// HiddenProviders is not a gate. It only removes buttons from /login/<slug>;
	// the same method still admits its people (docs/log/61 §61.15.9 + decision 14).
	HiddenProviders []string
	// AllowedCIDRs restricts which source networks may USE this tenant (docs/log/66,
	// ADR 0047). Empty = no restriction, and that is how the feature is switched off.
	// Unlike the fields above it belongs to the tenant_admin: it reaches nothing
	// outside this tenant. It is stored here rather than in limits JSON because it is
	// consulted on every request and these rules already have a cache in front of them.
	AllowedCIDRs []string
}

type TenantStore interface {
	EnsureDefaultTenant(ctx context.Context) (Tenant, error)
	CreateTenant(ctx context.Context, slug, name string) (Tenant, error)
	GetTenant(ctx context.Context, id string) (Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error)
	SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
	// DataRootCollisions lists stored tenant slugs and default-tenant user keys that
	// already share a directory under the data root (store_dataroot.go). Report-only.
	DataRootCollisions(ctx context.Context) ([]string, error)
	// DeleteTenant removes an EMPTY tenant (its leftover inactive memberships, its
	// configuration rows, and the row itself). Irreversible, super_admin only, and the
	// emptiness is proved by the handler — see deleteTenant in tenants.go for the five
	// refusals and why each one exists.
	DeleteTenant(ctx context.Context, tenantID string) error
	// SetTenantLogin stores the three CSV login rules (docs/log/61 §61.9.7). Values are
	// normalized by the caller (lowercased, deduped); this only writes them.
	// hiddenProviders is DISPLAY only (0042, docs/log/61 §61.15.9) — accepted methods to
	// leave off this tenant's login page, never a gate.
	SetTenantLogin(ctx context.Context, tenantID, allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders string) error
	// ListTenantLoginRules is the entry gate's bulk read: one query behind a short
	// TTL cache rather than a per-request lookup (§61.9.7).
	ListTenantLoginRules(ctx context.Context) ([]TenantLoginRules, error)
	// The tenant's source-network restriction (docs/log/66, ADR 0047). Kept apart from
	// SetTenantLogin because the owner differs: those three fields are the operator's,
	// this one is the tenant_admin's. "" = no restriction.
	GetTenantAllowedCIDRs(ctx context.Context, tenantID string) (string, error)
	SetTenantAllowedCIDRs(ctx context.Context, tenantID, cidrs string) error
	// GetTenantExternalMemory / SetTenantExternalMemory: the tenant-wide switch for AF memory
	// reached through /mcp from outside Agent Fleet (#2318). Unknown tenant = error.
	GetTenantExternalMemory(ctx context.Context, tenantID string) (bool, error)
	SetTenantExternalMemory(ctx context.Context, tenantID string, enabled bool) error
}

func (s *SQL) EnsureDefaultTenant(ctx context.Context) (Tenant, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES('default','default','Default','active','{}','shared',?)
		 ON CONFLICT(id) DO NOTHING`, NowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, "default")
}

// CreateTenant refuses a slug that would share a directory under the data root
// (checkTenantSlugFree); the error wraps ErrDataRootNameReserved or ErrDataRootNameTaken.
func (s *SQL) CreateTenant(ctx context.Context, slug, name string) (Tenant, error) {
	if err := s.checkTenantSlugFree(ctx, slug); err != nil {
		return Tenant{}, err
	}
	id := NewID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES(?, ?, ?, 'active', '{}', 'shared', ?)`, id, slug, name, NowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, id)
}

func (s *SQL) GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tenant WHERE slug=?`, slug).Scan(&id)
	if err == sql.ErrNoRows {
		return Tenant{}, false, nil
	}
	if err != nil {
		return Tenant{}, false, err
	}
	t, err := s.getTenant(ctx, id)
	return t, err == nil, err
}

func (s *SQL) GetTenant(ctx context.Context, id string) (Tenant, error) {
	return s.getTenant(ctx, id)
}

func (s *SQL) SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET limits=? WHERE id=?`, limitsJSON, tenantID)
	return err
}

func (s *SQL) getTenant(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	var keyRef sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at,
		        allowed_providers, auto_join_domains, allowed_domains, hidden_providers
		 FROM tenant WHERE id=?`, id).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &keyRef, &t.CreatedAt,
			&t.AllowedProviders, &t.AutoJoinDomains, &t.AllowedDomains, &t.HiddenProviders)
	t.KeyRef = keyRef.String
	return t, err
}

// SetTenantLogin writes the per-tenant login rules (docs/log/61 §61.9.7).
func (s *SQL) SetTenantLogin(ctx context.Context, tenantID, allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant SET allowed_providers=?, auto_join_domains=?, allowed_domains=?, hidden_providers=? WHERE id=?`,
		allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders, tenantID)
	return err
}

// SetTenantAllowedCIDRs writes the tenant's source-network restriction (docs/log/66).
// Separate from SetTenantLogin on purpose: that one is super_admin-only because its
// three fields reach outside the tenant, while this one is the tenant_admin's
// (ADR 0047 decision 6). Empty = no restriction.
func (s *SQL) SetTenantAllowedCIDRs(ctx context.Context, tenantID, cidrs string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET allowed_cidrs=? WHERE id=?`, cidrs, tenantID)
	return err
}

// GetTenantAllowedCIDRs reads it back for the editing screen (the request path reads
// it through the cache instead).
func (s *SQL) GetTenantAllowedCIDRs(ctx context.Context, tenantID string) (string, error) {
	var cidrs string
	err := s.db.QueryRowContext(ctx, `SELECT allowed_cidrs FROM tenant WHERE id=?`, tenantID).Scan(&cidrs)
	return cidrs, err
}

// SetTenantExternalMemory writes the tenant-wide switch for AF memory reached from outside
// Agent Fleet through /mcp (#2318).
func (s *SQL) SetTenantExternalMemory(ctx context.Context, tenantID string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET external_memory=? WHERE id=?`, v, tenantID)
	return err
}

// GetTenantExternalMemory reads it. An unknown tenant is an error, never "enabled": the caller
// that gates on it must fail closed.
func (s *SQL) GetTenantExternalMemory(ctx context.Context, tenantID string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT external_memory FROM tenant WHERE id=?`, tenantID).Scan(&v)
	return v != 0, err
}

// ListTenantLoginRules loads every tenant's rules in one query, already split into
// slices — the shape the entry-gate cache and the auto-join resolution want.
func (s *SQL) ListTenantLoginRules(ctx context.Context) ([]TenantLoginRules, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, allowed_providers, auto_join_domains, allowed_domains, hidden_providers, allowed_cidrs
		 FROM tenant WHERE status='active' ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantLoginRules
	for rows.Next() {
		var r TenantLoginRules
		var provs, autoJoin, allowed, hidden, cidrs string
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &provs, &autoJoin, &allowed, &hidden, &cidrs); err != nil {
			return nil, err
		}
		r.AllowedProviders = splitCSVLower(provs)
		r.AutoJoinDomains = splitDomainCSV(autoJoin)
		r.AllowedDomains = splitDomainCSV(allowed)
		r.HiddenProviders = splitCSVLower(hidden)
		r.AllowedCIDRs = splitCSV(cidrs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQL) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at,
		        allowed_providers, auto_join_domains, allowed_domains, hidden_providers
		 FROM tenant ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &t.KeyRef, &t.CreatedAt,
			&t.AllowedProviders, &t.AutoJoinDomains, &t.AllowedDomains, &t.HiddenProviders); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTenant removes an EMPTY tenant: whatever inactive memberships are left, the
// tenant's own configuration rows, and the tenant itself — in one transaction.
//
// "Empty" is the handler's job to prove (no active member, no workspace row, no internal
// git repo). This function does not re-check it, but it also cannot paper over it: it
// deliberately does not touch `workspace`, so a tenant that still had one would fail on
// the foreign key rather than orphan an EBS volume nobody is left to find.
//
// The tenant's history — audit_log, usage_daily, cloud_cost_daily — is deliberately kept,
// exactly as for a membership.
func (s *SQL) DeleteTenant(ctx context.Context, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM membership WHERE tenant_id=?`, tenantID)
	if err != nil {
		return err
	}
	var memberships []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		memberships = append(memberships, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range memberships {
		if err := s.deleteMembershipTx(ctx, tx, id); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`DELETE FROM mcp_server WHERE tenant_id=?`,
		`DELETE FROM tenant_idp WHERE tenant_id=?`,
		`DELETE FROM tenant_git_oauth WHERE tenant_id=?`,
		`DELETE FROM egress_allowlist WHERE tenant_id=?`,
		`DELETE FROM engine_access_policy WHERE tenant_id=?`,
		// A grant written for a membership deleted between the roster check and the insert
		// has no membership left to cascade from; the tenant id still reaches it.
		`DELETE FROM engine_access_grant WHERE tenant_id=?`,
		`DELETE FROM tenant_branch_rules WHERE tenant_id=?`,
		`DELETE FROM tenant_member_defaults WHERE tenant_id=?`,
		`DELETE FROM limit_board_tenant WHERE tenant_id=?`,
		// The login rules and allowed_cidrs are columns on tenant, so this one
		// statement takes them with it.
		`DELETE FROM tenant WHERE id=?`,
	} {
		if _, err = tx.ExecContext(ctx, stmt, tenantID); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return tx.Commit()
}
