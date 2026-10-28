package store

import (
	"context"
	"database/sql"
)

// TenantGitOAuth is one tenant's OAuth app for a git provider (docs/log/71, migration
// 0048). SecretEnc is opaque ciphertext here exactly like TenantIdP.SecretEnc: the
// SQL layer stores and returns it, tenant_git_oauth.go owns the sealing.
//
// ★ There is no status. Unlike TenantIdP, this row declares nothing about who
// anybody is — it only names the OAuth app a member's "connect GitHub / Bitbucket"
// button talks to — so it takes effect the moment the tenant_admin saves it
// (ADR0052 decision 3). An empty SecretEnc is normal for GitHub: its device flow
// authenticates with the client_id alone.
//
// Source, AppType, AppTypeBy and InstallURL are GitHub's (migration 0088): which app
// the row points at — a built-in one compiled into the binary, or the tenant's own —
// and, for the tenant's own, whether it is an OAuth App or a GitHub App.
type TenantGitOAuth struct {
	ID, TenantID, Provider string
	ClientID               string
	SecretEnc, KeyRef      string
	Source                 string
	AppType, AppTypeBy     string
	InstallURL             string
	// ScopeGroups is the comma-separated optional scope groups the administrator confirmed
	// the app grants (migration 0100); "" is none.
	ScopeGroups          string
	UpdatedBy            string
	CreatedAt, UpdatedAt string
}

// TenantGitOAuthStore is the per-tenant git provider OAuth app registry (docs/log/71).
// Every call carries tenant_id, the way TenantIdPStore does, so a tenant_admin of
// one tenant can never read or write another's app — and the secret in particular
// never leaves the tenant it was sealed for.
type TenantGitOAuthStore interface {
	ListTenantGitOAuth(ctx context.Context, tenantID string) ([]TenantGitOAuth, error)
	GetTenantGitOAuth(ctx context.Context, tenantID, provider string) (TenantGitOAuth, bool, error)
	// PutTenantGitOAuth is an upsert on (tenant_id, provider): a tenant has one app
	// per host, so "create" and "edit" are the same act and splitting them would only
	// invite a duplicate row the unique index then refuses.
	PutTenantGitOAuth(ctx context.Context, row TenantGitOAuth) error
	DeleteTenantGitOAuth(ctx context.Context, tenantID, provider string) error
	// SetTenantGitOAuthAppType records what a client_id turned out to be. It writes only
	// while the row still names clientID: the lesson comes from a token minted earlier,
	// and an administrator may have pointed the row at another app in the meantime.
	SetTenantGitOAuthAppType(ctx context.Context, tenantID, provider, clientID, appType, by string) error
}

// --- tenant-owned git provider OAuth apps (docs/log/71 + ADR0052) ------------------

const tenantGitOAuthCols = `SELECT id, tenant_id, provider, client_id, secret_enc, key_ref,
       source, app_type, app_type_by, install_url, scope_groups,
       updated_by, created_at, updated_at FROM tenant_git_oauth`

func scanTenantGitOAuth(sc scanner) (TenantGitOAuth, error) {
	var g TenantGitOAuth
	err := sc.Scan(&g.ID, &g.TenantID, &g.Provider, &g.ClientID, &g.SecretEnc, &g.KeyRef,
		&g.Source, &g.AppType, &g.AppTypeBy, &g.InstallURL, &g.ScopeGroups,
		&g.UpdatedBy, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func (s *SQL) ListTenantGitOAuth(ctx context.Context, tenantID string) ([]TenantGitOAuth, error) {
	rows, err := s.db.QueryContext(ctx, tenantGitOAuthCols+` WHERE tenant_id=? ORDER BY provider`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantGitOAuth
	for rows.Next() {
		g, err := scanTenantGitOAuth(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *SQL) GetTenantGitOAuth(ctx context.Context, tenantID, provider string) (TenantGitOAuth, bool, error) {
	g, err := scanTenantGitOAuth(s.db.QueryRowContext(ctx,
		tenantGitOAuthCols+` WHERE tenant_id=? AND provider=?`, tenantID, provider))
	if err == sql.ErrNoRows {
		return TenantGitOAuth{}, false, nil
	}
	return g, err == nil, err
}

// PutTenantGitOAuth upserts on the (tenant_id, provider) unique index. created_at is
// left alone on conflict so the row keeps the date it was first registered.
func (s *SQL) PutTenantGitOAuth(ctx context.Context, g TenantGitOAuth) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_git_oauth(id, tenant_id, provider, client_id, secret_enc, key_ref,
		   source, app_type, app_type_by, install_url, scope_groups,
		   updated_by, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(tenant_id, provider) DO UPDATE SET
		   client_id=excluded.client_id, secret_enc=excluded.secret_enc, key_ref=excluded.key_ref,
		   source=excluded.source, app_type=excluded.app_type, app_type_by=excluded.app_type_by,
		   install_url=excluded.install_url, scope_groups=excluded.scope_groups,
		   updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		g.ID, g.TenantID, g.Provider, g.ClientID, g.SecretEnc, g.KeyRef,
		g.Source, g.AppType, g.AppTypeBy, g.InstallURL, g.ScopeGroups,
		g.UpdatedBy, g.CreatedAt, g.UpdatedAt)
	return err
}

func (s *SQL) SetTenantGitOAuthAppType(ctx context.Context, tenantID, provider, clientID, appType, by string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant_git_oauth SET app_type=?, app_type_by=? WHERE tenant_id=? AND provider=? AND client_id=?`,
		appType, by, tenantID, provider, clientID)
	return err
}

func (s *SQL) DeleteTenantGitOAuth(ctx context.Context, tenantID, provider string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tenant_git_oauth WHERE tenant_id=? AND provider=?`, tenantID, provider)
	return err
}
