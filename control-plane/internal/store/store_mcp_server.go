package store

import (
	"context"
	"database/sql"
)

// MCPServerRow is one tenant-distributed MCP server definition (docs/log/48 P4 +
// ADR0031). It is deliberately NOT the agent's full ServerDef: there is no Command /
// Args / Env, because a tenant-distributed stdio server would be an admin running an
// arbitrary command in every member's container. The table has no such columns either,
// so the refusal cannot be relaxed by editing one validation branch.
//
// HeadersEnc is the header map sealed by the tenant key custodian (KeyRef names the
// key), or plaintext JSON with an empty KeyRef on a deployment with no master key —
// the same degradation the Agent's secret store makes in dev. Targets and Kinds are
// comma lists on the wire to the DB ("assistant,session"; empty Kinds = every kind).
//
// UserSecret=1 distributes the URL and the header NAMES only: each member supplies the
// values into their own encrypted store (docs/log/48 §5.2). It exists because a token in a
// distributed header is readable in plaintext by every member of the tenant, which
// container isolation cannot prevent.
type MCPServerRow struct {
	ID, TenantID        string
	Name, Label         string
	Transport           string // always "http" — stdio is refused (ADR0031 decision 2)
	URL                 string
	HeadersEnc, KeyRef  string
	Targets, Kinds      string
	TimeoutMS           int
	Enabled, UserSecret bool
	Auth                string // "" or "oauth" (ADR 0119): an oauth row has no headers
	// The tenant's pre-registered client for an oauth row; OAuthSecretEnc is sealed under
	// OAuthKeyRef like HeadersEnc, and empty for a public client.
	OAuthIssuer, OAuthClientID  string
	OAuthSecretEnc, OAuthKeyRef string
	CreatedBy                   string
	CreatedAt, UpdatedAt        string
}

// MCPServerStore is the tenant-distributed MCP server registry (docs/log/48 P4 +
// ADR0031). Rows are tenant-scoped: every mutation carries tenant_id in the WHERE so a
// tenant_admin of one tenant can never reach another's definitions even if an id leaks.
// HeadersEnc is stored and returned as opaque ciphertext — decryption is the handler's
// job (mcp_server.go), which keeps the crypto out of the SQL layer.
type MCPServerStore interface {
	ListMCPServers(ctx context.Context, tenantID string) ([]MCPServerRow, error)
	GetMCPServer(ctx context.Context, tenantID, id string) (MCPServerRow, bool, error)
	CreateMCPServer(ctx context.Context, row MCPServerRow) error
	UpdateMCPServer(ctx context.Context, row MCPServerRow) error
	DeleteMCPServer(ctx context.Context, tenantID, id string) error
}

// --- Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031) ----------------------
//
// Every statement carries tenant_id, including the ones that already have the primary
// key in the WHERE. An id is opaque but not secret (it travels to the Console and into
// each member's cache), so scoping by tenant is what actually stops a tenant_admin of
// one tenant from reaching another's row by guessing or replaying an id.

const mcpServerCols = `SELECT id, tenant_id, name, label, transport, url, headers_enc, key_ref,
	targets, kinds, timeout_ms, enabled, user_secret, auth, oauth_issuer, oauth_client_id, oauth_secret_enc, oauth_key_ref,
	created_by, created_at, updated_at FROM mcp_server`

func scanMCPServer(row scanner) (MCPServerRow, error) {
	var m MCPServerRow
	var enabled, userSecret int
	err := row.Scan(&m.ID, &m.TenantID, &m.Name, &m.Label, &m.Transport, &m.URL, &m.HeadersEnc, &m.KeyRef,
		&m.Targets, &m.Kinds, &m.TimeoutMS, &enabled, &userSecret, &m.Auth, &m.OAuthIssuer, &m.OAuthClientID, &m.OAuthSecretEnc, &m.OAuthKeyRef,
		&m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	m.Enabled = enabled != 0
	m.UserSecret = userSecret != 0
	return m, err
}

func (s *SQL) ListMCPServers(ctx context.Context, tenantID string) ([]MCPServerRow, error) {
	rows, err := s.db.QueryContext(ctx, mcpServerCols+` WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServerRow
	for rows.Next() {
		m, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQL) GetMCPServer(ctx context.Context, tenantID, id string) (MCPServerRow, bool, error) {
	m, err := scanMCPServer(s.db.QueryRowContext(ctx, mcpServerCols+` WHERE tenant_id=? AND id=?`, tenantID, id))
	if err == sql.ErrNoRows {
		return MCPServerRow{}, false, nil
	}
	return m, err == nil, err
}

func (s *SQL) CreateMCPServer(ctx context.Context, m MCPServerRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_server(id, tenant_id, name, label, transport, url, headers_enc, key_ref,
		   targets, kinds, timeout_ms, enabled, user_secret, auth, oauth_issuer, oauth_client_id, oauth_secret_enc, oauth_key_ref,
		   created_by, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.TenantID, m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.Auth, m.OAuthIssuer, m.OAuthClientID, m.OAuthSecretEnc, m.OAuthKeyRef,
		m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	return err
}

func (s *SQL) UpdateMCPServer(ctx context.Context, m MCPServerRow) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_server SET name=?, label=?, transport=?, url=?, headers_enc=?, key_ref=?,
		   targets=?, kinds=?, timeout_ms=?, enabled=?, user_secret=?, auth=?,
		   oauth_issuer=?, oauth_client_id=?, oauth_secret_enc=?, oauth_key_ref=?, updated_at=?
		 WHERE tenant_id=? AND id=?`,
		m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.Auth,
		m.OAuthIssuer, m.OAuthClientID, m.OAuthSecretEnc, m.OAuthKeyRef, m.UpdatedAt,
		m.TenantID, m.ID)
	return err
}

func (s *SQL) DeleteMCPServer(ctx context.Context, tenantID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_server WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}
