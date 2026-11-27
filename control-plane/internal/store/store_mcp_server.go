package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	// OAuthScopes is the scope list of an oauth row's sign-in, space-separated; "" = none of its
	// own (ADR 0146 decision 2).
	OAuthScopes          string
	Elicitation          string // "" or "relay"; relay is opt-in for OAuth definitions
	CreatedBy            string
	CreatedAt, UpdatedAt string
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
	CreateMCPServers(ctx context.Context, tenantID string, rows []MCPServerRow) (conflict string, err error)
	UpdateMCPServer(ctx context.Context, row MCPServerRow) error
	DeleteMCPServer(ctx context.Context, tenantID, id string) error
}

// ErrMCPServerNameTaken means another definition in the tenant uses the name, ignoring case.
var ErrMCPServerNameTaken = errors.New("MCP server name already exists")

// ErrMCPTenantNotFound means the tenant disappeared before the definition could be saved.
var ErrMCPTenantNotFound = errors.New("MCP server tenant not found")

// --- Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031) ----------------------
//
// Every statement carries tenant_id, including the ones that already have the primary
// key in the WHERE. An id is opaque but not secret (it travels to the Console and into
// each member's cache), so scoping by tenant is what actually stops a tenant_admin of
// one tenant from reaching another's row by guessing or replaying an id.

const mcpServerCols = `SELECT id, tenant_id, name, label, transport, url, headers_enc, key_ref,
	targets, kinds, timeout_ms, enabled, user_secret, auth, oauth_issuer, oauth_client_id, oauth_secret_enc, oauth_key_ref, oauth_scopes, elicitation,
	created_by, created_at, updated_at FROM mcp_server`

func scanMCPServer(row scanner) (MCPServerRow, error) {
	var m MCPServerRow
	var enabled, userSecret int
	err := row.Scan(&m.ID, &m.TenantID, &m.Name, &m.Label, &m.Transport, &m.URL, &m.HeadersEnc, &m.KeyRef,
		&m.Targets, &m.Kinds, &m.TimeoutMS, &enabled, &userSecret, &m.Auth, &m.OAuthIssuer, &m.OAuthClientID, &m.OAuthSecretEnc, &m.OAuthKeyRef, &m.OAuthScopes,
		&m.Elicitation, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
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
	name, err := s.CreateMCPServers(ctx, m.TenantID, []MCPServerRow{m})
	if name != "" {
		return ErrMCPServerNameTaken
	}
	return err
}

// CreateMCPServers publishes the entire set at once so members never see a partial preset.
func (s *SQL) CreateMCPServers(ctx context.Context, tenantID string, rows []MCPServerRow) (string, error) {
	tx, err := s.beginMCPServerWrite(ctx, tenantID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, row := range rows {
		res, err := s.insertMCPServer(ctx, tx, tenantID, row)
		if err != nil {
			return "", err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return "", err
		}
		if n == 0 {
			return row.Name, nil
		}
	}
	return "", tx.Commit()
}

func (s *SQL) beginMCPServerWrite(ctx context.Context, tenantID string) (*sqlTx, error) {
	tx, err := s.beginTenantMCPMutation(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tenant WHERE id=?)`, tenantID).Scan(&exists)
	if err != nil || !exists {
		_ = tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, ErrMCPTenantNotFound
	}
	return tx, nil
}

// beginTenantMCPMutation serializes definition saves and tenant deletion. Postgres uses a
// separate lock namespace so tenant deletion does not change its row-lock order relative to
// other tenant operations. READ COMMITTED sees the winner's rows after waiting for its lock.
// SQLite acquires the database writer lock before reading the tenant or any of its children.
func (s *SQL) beginTenantMCPMutation(ctx context.Context, tenantID string) (*sqlTx, error) {
	var opts *sql.TxOptions
	lock := `UPDATE tenant SET status=status WHERE id=?`
	key := tenantID
	if s.dialect == "postgres" {
		opts = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
		lock = `SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`
		key = "af:mcp-tenant:" + tenantID
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, lock, key); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// Server names use ASCII. Keep case folding independent of the database locale.
func (s *SQL) mcpServerNameSQL() string {
	if s.dialect == "postgres" {
		return `lower(name COLLATE "C")`
	}
	return `lower(name)`
}

func (s *SQL) insertMCPServer(ctx context.Context, tx *sqlTx, tenantID string, m MCPServerRow) (sql.Result, error) {
	return tx.ExecContext(ctx,
		`INSERT INTO mcp_server(id, tenant_id, name, label, transport, url, headers_enc, key_ref,
		   targets, kinds, timeout_ms, enabled, user_secret, auth, oauth_issuer, oauth_client_id, oauth_secret_enc, oauth_key_ref, oauth_scopes,
		   elicitation, created_by, created_at, updated_at)
		 SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
		 WHERE NOT EXISTS (SELECT 1 FROM mcp_server WHERE tenant_id=? AND `+s.mcpServerNameSQL()+`=?)
		 ON CONFLICT(tenant_id, name) DO NOTHING`,
		m.ID, tenantID, m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.Auth, m.OAuthIssuer, m.OAuthClientID, m.OAuthSecretEnc, m.OAuthKeyRef, m.OAuthScopes,
		m.Elicitation, m.CreatedBy, m.CreatedAt, m.UpdatedAt, tenantID, strings.ToLower(m.Name))
}

func (s *SQL) UpdateMCPServer(ctx context.Context, m MCPServerRow) error {
	tx, err := s.beginMCPServerWrite(ctx, m.TenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`UPDATE mcp_server SET name=?, label=?, transport=?, url=?, headers_enc=?, key_ref=?,
		   targets=?, kinds=?, timeout_ms=?, enabled=?, user_secret=?, auth=?,
		   oauth_issuer=?, oauth_client_id=?, oauth_secret_enc=?, oauth_key_ref=?, oauth_scopes=?, elicitation=?, updated_at=?
		 WHERE tenant_id=? AND id=?
		 AND NOT EXISTS (SELECT 1 FROM mcp_server WHERE tenant_id=? AND id<>? AND `+s.mcpServerNameSQL()+`=?)`,
		m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.Auth,
		m.OAuthIssuer, m.OAuthClientID, m.OAuthSecretEnc, m.OAuthKeyRef, m.OAuthScopes, m.Elicitation, m.UpdatedAt,
		m.TenantID, m.ID, m.TenantID, m.ID, strings.ToLower(m.Name))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_server WHERE tenant_id=? AND id=?)`, m.TenantID, m.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrMCPServerNameTaken
		}
	}
	return tx.Commit()
}

func (s *SQL) DeleteMCPServer(ctx context.Context, tenantID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_server WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}
