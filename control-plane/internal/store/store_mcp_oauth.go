package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MCPOAuthClient is a dynamically registered client (RFC 7591) of the /mcp authorization
// server (ADR 0118). RedirectURIs is a JSON array. CIMD clients are not stored.
type MCPOAuthClient struct {
	ClientID, Name, RedirectURIs string
	CreatedAt                    int64
}

// MCPOAuthGrant is one connection of one client to one membership: what the member sees and
// revokes. Times are unix seconds; RevokedAt is 0 while the grant is live.
type MCPOAuthGrant struct {
	ID, ClientID, ClientName, RedirectHost, IdentityID, MembershipID, TenantID string
	Scope, Resource, CodeHash                                                  string
	CreatedAt, LastUsedAt, IdleExpiresAt, AbsExpiresAt, RevokedAt              int64
	RevokedReason                                                              string
}

const (
	MCPOAuthConsent    = "consent"
	MCPOAuthCode       = "code"
	MCPOAuthAccessKind = "access"
	MCPOAuthRefresh    = "refresh"
)

// MCPOAuthToken is a consent ticket, authorization code, access token or refresh token. Hash
// is the SHA-256 of the value; the value itself is never stored.
type MCPOAuthToken struct {
	Hash, Kind, GrantID, ClientID, ClientName, IdentityID, MembershipID string
	RedirectURI, Challenge, Scope, Resource, State                      string
	ExpiresAt, UsedAt                                                   int64
}

// MCPOAuthSpend is what ConsumeMCPOAuthToken found.
type MCPOAuthSpend int

const (
	// MCPOAuthSpendOK: the row was live and is now spent.
	MCPOAuthSpendOK MCPOAuthSpend = iota
	// MCPOAuthSpendUnknown: no such row of that kind, or it has expired.
	MCPOAuthSpendUnknown
	// MCPOAuthSpendReplayed: the row had been spent already.
	MCPOAuthSpendReplayed
)

// MCPOAuthAccess is a live access token joined to its live grant. Scope is the token's own,
// which a refresh may have narrowed below the grant's.
type MCPOAuthAccess struct {
	GrantID, IdentityID, MembershipID, Scope, Resource string
}

// ErrMCPOAuthDisabled rejects a grant whose tenant disabled OAuth during issuance.
var ErrMCPOAuthDisabled = errors.New("mcp oauth: connection disabled or revoked")

// ErrMCPOAuthClientCap: the registration table already holds the maximum number of clients.
var ErrMCPOAuthClientCap = errors.New("mcp oauth: client registration limit reached")

// MCPOAuthStore holds the /mcp authorization server's clients, grants and tokens.
type MCPOAuthStore interface {
	GetTenantMCPOAuth(ctx context.Context, tenantID string) (bool, error)
	// SetTenantMCPOAuth atomically gates issuance, revokes grants and audits the change.
	SetTenantMCPOAuth(ctx context.Context, tenantID string, enabled bool, actorID string, now int64) error
	// CreateMCPOAuthClient returns ErrMCPOAuthClientCap at max registrations (counted in the
	// same transaction as the insert).
	CreateMCPOAuthClient(ctx context.Context, c MCPOAuthClient, max int) error
	GetMCPOAuthClient(ctx context.Context, clientID string) (MCPOAuthClient, bool, error)
	// PruneMCPOAuth deletes clients registered before `clientsBefore` that never produced a
	// grant, grants that ended before `grantsBefore`, and tokens expired by `now`.
	PruneMCPOAuth(ctx context.Context, now, clientsBefore, grantsBefore int64) error

	InsertMCPOAuthToken(ctx context.Context, t MCPOAuthToken) error
	// ConsumeMCPOAuthToken spends a row of the given kind exactly once, however many callers
	// race for it: one conditional UPDATE decides, and its RowsAffected is the whole guarantee.
	ConsumeMCPOAuthToken(ctx context.Context, hash, kind string, now int64) (MCPOAuthToken, MCPOAuthSpend, error)
	// MarkMCPOAuthCodeReplayed records on the code row that it was presented again, so a grant
	// being minted from it at this moment can see that and take itself down (state = replayed).
	MarkMCPOAuthCodeReplayed(ctx context.Context, hash string) error
	GetMCPOAuthTokenByHash(ctx context.Context, hash string, now int64) (MCPOAuthToken, bool, error)
	// GetMCPOAuthAccess resolves a live access token whose grant is live, unexpired and not idle.
	GetMCPOAuthAccess(ctx context.Context, hash string, now int64) (MCPOAuthAccess, bool, error)

	CreateMCPOAuthGrant(ctx context.Context, g MCPOAuthGrant) error
	GetMCPOAuthGrant(ctx context.Context, id string) (MCPOAuthGrant, bool, error)
	// TouchMCPOAuthGrant records use and pushes the idle deadline to now+idle, never past the
	// absolute one.
	TouchMCPOAuthGrant(ctx context.Context, id string, now, idle int64) error
	// RevokeMCPOAuthGrant ends a grant and deletes its tokens; false when it was not live.
	RevokeMCPOAuthGrant(ctx context.Context, id, reason string, now int64) (bool, error)
	// RevokeMCPOAuthGrantsByCode ends the grants minted from a code that was presented again.
	RevokeMCPOAuthGrantsByCode(ctx context.Context, codeHash, reason string, now int64) ([]string, error)
	// ListMCPOAuthGrantsByIdentity returns the identity's live grants, newest first (the seam
	// for a connected-apps list).
	ListMCPOAuthGrantsByIdentity(ctx context.Context, identityID string, now int64) ([]MCPOAuthGrant, error)
	// RevokeMCPOAuthGrantOf atomically revokes and audits the owner's connection.
	// A losing concurrent request returns false without an audit row.
	RevokeMCPOAuthGrantOf(ctx context.Context, id, identityID, reason string, now int64) (bool, error)
}

func (s *SQL) CreateMCPOAuthClient(ctx context.Context, c MCPOAuthClient, max int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialises registrations across connections and Control Planes; see mcp_oauth_lock.
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_lock SET id=id WHERE id='client_cap'`); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_client`).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return ErrMCPOAuthClientCap
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO mcp_oauth_client(client_id, name, redirect_uris, created_at) VALUES(?, ?, ?, ?)`,
		c.ClientID, c.Name, c.RedirectURIs, c.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) GetMCPOAuthClient(ctx context.Context, clientID string) (MCPOAuthClient, bool, error) {
	var c MCPOAuthClient
	err := s.db.QueryRowContext(ctx,
		`SELECT client_id, name, redirect_uris, created_at FROM mcp_oauth_client WHERE client_id=?`, clientID).
		Scan(&c.ClientID, &c.Name, &c.RedirectURIs, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthClient{}, false, nil
	}
	return c, err == nil, err
}

func (s *SQL) PruneMCPOAuth(ctx context.Context, now, clientsBefore, grantsBefore int64) error {
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM mcp_oauth_token WHERE expires_at<=?`, []any{now}},
		// A grant that ended long ago: revoked, or past its absolute lifetime, or idle out.
		{`DELETE FROM mcp_oauth_grant WHERE (revoked_at<>0 AND revoked_at<?) OR abs_expires_at<? OR idle_expires_at<?`,
			[]any{grantsBefore, grantsBefore, grantsBefore}},
		{`DELETE FROM mcp_oauth_client WHERE created_at<? AND NOT EXISTS
		    (SELECT 1 FROM mcp_oauth_grant g WHERE g.client_id=mcp_oauth_client.client_id)`, []any{clientsBefore}},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQL) InsertMCPOAuthToken(ctx context.Context, t MCPOAuthToken) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_oauth_token(hash, kind, grant_id, client_id, client_name, identity_id, membership_id,
		   redirect_uri, challenge, scope, resource, state, expires_at, used_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		t.Hash, t.Kind, t.GrantID, t.ClientID, t.ClientName, t.IdentityID, t.MembershipID,
		t.RedirectURI, t.Challenge, t.Scope, t.Resource, t.State, t.ExpiresAt)
	return err
}

const mcpOAuthTokenCols = `hash, kind, grant_id, client_id, client_name, identity_id, membership_id,
	redirect_uri, challenge, scope, resource, state, expires_at, used_at`

func scanMCPOAuthToken(row interface{ Scan(...any) error }) (MCPOAuthToken, error) {
	var t MCPOAuthToken
	err := row.Scan(&t.Hash, &t.Kind, &t.GrantID, &t.ClientID, &t.ClientName, &t.IdentityID, &t.MembershipID,
		&t.RedirectURI, &t.Challenge, &t.Scope, &t.Resource, &t.State, &t.ExpiresAt, &t.UsedAt)
	return t, err
}

func (s *SQL) ConsumeMCPOAuthToken(ctx context.Context, hash, kind string, now int64) (MCPOAuthToken, MCPOAuthSpend, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE mcp_oauth_token SET used_at=? WHERE hash=? AND kind=? AND used_at=0 AND expires_at>?`,
		now, hash, kind, now)
	if err != nil {
		return MCPOAuthToken{}, MCPOAuthSpendUnknown, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return MCPOAuthToken{}, MCPOAuthSpendUnknown, err
	}
	t, err := scanMCPOAuthToken(s.db.QueryRowContext(ctx,
		`SELECT `+mcpOAuthTokenCols+` FROM mcp_oauth_token WHERE hash=? AND kind=? AND expires_at>?`, hash, kind, now))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthToken{}, MCPOAuthSpendUnknown, nil
	}
	if err != nil {
		return MCPOAuthToken{}, MCPOAuthSpendUnknown, err
	}
	if n == 1 {
		return t, MCPOAuthSpendOK, nil
	}
	return t, MCPOAuthSpendReplayed, nil
}

func (s *SQL) MarkMCPOAuthCodeReplayed(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_oauth_token SET state='replayed' WHERE hash=? AND kind=?`, hash, MCPOAuthCode)
	return err
}

func (s *SQL) GetMCPOAuthTokenByHash(ctx context.Context, hash string, now int64) (MCPOAuthToken, bool, error) {
	t, err := scanMCPOAuthToken(s.db.QueryRowContext(ctx,
		`SELECT `+mcpOAuthTokenCols+` FROM mcp_oauth_token WHERE hash=? AND expires_at>?`, hash, now))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthToken{}, false, nil
	}
	return t, err == nil, err
}

func (s *SQL) GetMCPOAuthAccess(ctx context.Context, hash string, now int64) (MCPOAuthAccess, bool, error) {
	var a MCPOAuthAccess
	err := s.db.QueryRowContext(ctx,
		`SELECT g.id, g.identity_id, g.membership_id, t.scope, t.resource
		 FROM mcp_oauth_token t JOIN mcp_oauth_grant g ON g.id=t.grant_id
		 WHERE t.hash=? AND t.kind=? AND t.expires_at>? AND g.revoked_at=0
		   AND g.abs_expires_at>? AND g.idle_expires_at>?
		   AND NOT EXISTS (SELECT 1 FROM tenant WHERE id=g.tenant_id AND mcp_oauth_disabled<>0)`,
		hash, MCPOAuthAccessKind, now, now, now).
		Scan(&a.GrantID, &a.IdentityID, &a.MembershipID, &a.Scope, &a.Resource)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthAccess{}, false, nil
	}
	return a, err == nil, err
}

const mcpOAuthGrantCols = `id, client_id, client_name, redirect_host, identity_id, membership_id, tenant_id,
	scope, resource, code_hash, created_at, last_used_at, idle_expires_at, abs_expires_at, revoked_at, revoked_reason`

func scanMCPOAuthGrant(row interface{ Scan(...any) error }) (MCPOAuthGrant, error) {
	var g MCPOAuthGrant
	err := row.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.RedirectHost, &g.IdentityID, &g.MembershipID, &g.TenantID,
		&g.Scope, &g.Resource, &g.CodeHash, &g.CreatedAt, &g.LastUsedAt, &g.IdleExpiresAt, &g.AbsExpiresAt,
		&g.RevokedAt, &g.RevokedReason)
	return g, err
}

func (s *SQL) CreateMCPOAuthGrant(ctx context.Context, g MCPOAuthGrant) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Every grant creation writes the tenant row, even unchanged, to take PostgreSQL's
	// row lock and serialize with disabling across CP instances (SQLite takes a write lock).
	if _, err = tx.ExecContext(ctx, `UPDATE tenant SET mcp_oauth_disabled=mcp_oauth_disabled WHERE id=?`, g.TenantID); err != nil {
		return err
	}
	var blocked int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenant WHERE id=? AND mcp_oauth_disabled<>0`, g.TenantID).Scan(&blocked); err != nil {
		return err
	}
	if blocked != 0 {
		return ErrMCPOAuthDisabled
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_oauth_token WHERE hash=? AND state='tenant-disabled'`, g.CodeHash).Scan(&blocked); err != nil {
		return err
	}
	if blocked != 0 {
		return ErrMCPOAuthDisabled
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO mcp_oauth_grant(`+mcpOAuthGrantCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.ClientID, g.ClientName, g.RedirectHost, g.IdentityID, g.MembershipID, g.TenantID,
		g.Scope, g.Resource, g.CodeHash, g.CreatedAt, g.LastUsedAt, g.IdleExpiresAt, g.AbsExpiresAt,
		g.RevokedAt, g.RevokedReason)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) GetMCPOAuthGrant(ctx context.Context, id string) (MCPOAuthGrant, bool, error) {
	g, err := scanMCPOAuthGrant(s.db.QueryRowContext(ctx,
		`SELECT `+mcpOAuthGrantCols+` FROM mcp_oauth_grant WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthGrant{}, false, nil
	}
	return g, err == nil, err
}

func (s *SQL) TouchMCPOAuthGrant(ctx context.Context, id string, now, idle int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_oauth_grant SET last_used_at=?,
		   idle_expires_at=CASE WHEN abs_expires_at<? THEN abs_expires_at ELSE ? END
		 WHERE id=? AND revoked_at=0`, now, now+idle, now+idle, id)
	return err
}

func (s *SQL) RevokeMCPOAuthGrant(ctx context.Context, id, reason string, now int64) (bool, error) {
	return s.revokeMCPOAuthGrant(ctx, `id=?`, []any{id}, reason, now)
}

func (s *SQL) RevokeMCPOAuthGrantOf(ctx context.Context, id, identityID, reason string, now int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE mcp_oauth_grant SET revoked_at=?, revoked_reason=? WHERE id=? AND identity_id=? AND revoked_at=0`, now, reason, id, identityID)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	// The mutation stays invisible until the audit commits with it; a failed audit rolls it back.
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at)
   SELECT ?, tenant_id, 'user', ?, 'mcp.oauth.grant', id, ?, ? FROM mcp_oauth_grant WHERE id=? AND identity_id=?`, NewID(), identityID, reason, NowTS(), id, identityID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_token WHERE grant_id=?`, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQL) revokeMCPOAuthGrant(ctx context.Context, where string, args []any, reason string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE mcp_oauth_grant SET revoked_at=?, revoked_reason=? WHERE `+where+` AND revoked_at=0`,
		append([]any{now, reason}, args...)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	// The grant is already dead to GetMCPOAuthAccess; the delete only keeps the table small.
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM mcp_oauth_token WHERE grant_id IN (SELECT id FROM mcp_oauth_grant WHERE `+where+`)`, args...)
	return true, err
}

func (s *SQL) RevokeMCPOAuthGrantsByCode(ctx context.Context, codeHash, reason string, now int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM mcp_oauth_grant WHERE code_hash=? AND revoked_at=0`, codeHash)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.RevokeMCPOAuthGrant(ctx, id, reason, now); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func (s *SQL) ListMCPOAuthGrantsByIdentity(ctx context.Context, identityID string, now int64) ([]MCPOAuthGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mcpOAuthGrantCols+` FROM mcp_oauth_grant
		 WHERE identity_id=? AND revoked_at=0 AND abs_expires_at>? AND idle_expires_at>?
		 ORDER BY created_at DESC`, identityID, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPOAuthGrant
	for rows.Next() {
		g, err := scanMCPOAuthGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetTenantMCPOAuth reads the tenant's OAuth policy without caching, including on token issuance.
func (s *SQL) GetTenantMCPOAuth(ctx context.Context, tenantID string) (bool, error) {
	var disabled int
	err := s.db.QueryRowContext(ctx, `SELECT mcp_oauth_disabled FROM tenant WHERE id=?`, tenantID).Scan(&disabled)
	return disabled == 0 && err == nil, err
}

func (s *SQL) SetTenantMCPOAuth(ctx context.Context, tenantID string, enabled bool, actorID string, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	disabled := 1
	if enabled {
		disabled = 0
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tenant SET mcp_oauth_disabled=? WHERE id=?`, disabled, tenantID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at) VALUES(?, ?, 'user', ?, 'tenant.mcp_oauth', ?, ?, ?)`, NewID(), tenantID, actorID, tenantID, fmt.Sprintf("enabled=%t", enabled), NowTS()); err != nil {
		return err
	}
	if !enabled {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM mcp_oauth_grant WHERE tenant_id=? AND revoked_at=0`, tenantID)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at) VALUES(?, ?, 'user', ?, 'mcp.oauth.grant', ?, 'revoked reason=tenant-disabled', ?)`, NewID(), tenantID, actorID, id, NowTS()); err != nil {
				return err
			}
		}
		// Preserve the mark even after re-enabling: a code exchange already in flight must fail.
		if _, err = tx.ExecContext(ctx, `UPDATE mcp_oauth_token SET state='tenant-disabled' WHERE kind=? AND membership_id IN (SELECT id FROM membership WHERE tenant_id=?)`, MCPOAuthCode, tenantID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mcp_oauth_grant SET revoked_at=?, revoked_reason='tenant-disabled' WHERE tenant_id=? AND revoked_at=0`, now, tenantID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
