package store

import (
	"context"
	"database/sql"
)

// PAT is a Personal Access Token (docs/decisions/0006, P3-6). It authenticates
// the MCP endpoint. role is NOT stored — it is resolved live from identity +
// membership at call time so a demotion/revocation takes effect immediately.
// scope is fixed at issuance (read | write | admin:dangerous), clamped to the
// issuer's ceiling. Timestamps are RFC3339 ("" = unset). Resource is the endpoint the token
// is for (PATResourceMCP or PATResourceMemory); "" on create means PATResourceMCP.
type PAT struct {
	ID, IdentityID, MembershipID, Scope, Name   string
	CreatedAt, ExpiresAt, RevokedAt, LastUsedAt string
	Resource                                    string
}

// The two endpoints a PAT can be bound to (#2764). Each endpoint refuses the other's tokens.
const (
	PATResourceMCP    = "mcp"    // /mcp: sessions, memory and admin tools
	PATResourceMemory = "memory" // /mcp/memory: the external memory tools only
)

// PATStore holds Personal Access Tokens for the MCP endpoint (docs/decisions/0006, P3-6).
type PATStore interface {
	CreatePAT(ctx context.Context, p PAT, tokenHash string) error
	GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error)
	ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error)
	RevokePAT(ctx context.Context, id, identityID string) error
	TouchPAT(ctx context.Context, id string) error
}

// --- Personal Access Tokens (docs/decisions/0006, P3-6) ---

func (s *SQL) CreatePAT(ctx context.Context, p PAT, tokenHash string) error {
	if p.Resource == "" {
		p.Resource = PATResourceMCP
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pat(id, identity_id, membership_id, token_hash, scope, name, created_at, expires_at, resource)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.IdentityID, nullable(p.MembershipID), tokenHash, p.Scope, p.Name, p.CreatedAt, nullable(p.ExpiresAt), p.Resource)
	return err
}

func (s *SQL) GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error) {
	var p PAT
	var mid, exp, rev, last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, identity_id, COALESCE(membership_id,''), scope, name, created_at,
		        expires_at, revoked_at, last_used_at, resource FROM pat WHERE token_hash=?`, tokenHash).
		Scan(&p.ID, &p.IdentityID, &mid, &p.Scope, &p.Name, &p.CreatedAt, &exp, &rev, &last, &p.Resource)
	if err == sql.ErrNoRows {
		return PAT{}, false, nil
	}
	if err != nil {
		return PAT{}, false, err
	}
	p.MembershipID, p.ExpiresAt, p.RevokedAt, p.LastUsedAt = mid.String, exp.String, rev.String, last.String
	return p, true, nil
}

func (s *SQL) ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, identity_id, COALESCE(membership_id,''), scope, name, created_at,
		        COALESCE(expires_at,''), COALESCE(revoked_at,''), COALESCE(last_used_at,''), resource
		 FROM pat WHERE identity_id=? ORDER BY created_at DESC`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PAT
	for rows.Next() {
		var p PAT
		if err := rows.Scan(&p.ID, &p.IdentityID, &p.MembershipID, &p.Scope, &p.Name,
			&p.CreatedAt, &p.ExpiresAt, &p.RevokedAt, &p.LastUsedAt, &p.Resource); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokePAT marks a token revoked, scoped to its owner so a user can only revoke
// their own tokens.
func (s *SQL) RevokePAT(ctx context.Context, id, identityID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pat SET revoked_at=? WHERE id=? AND identity_id=? AND revoked_at IS NULL`,
		NowTS(), id, identityID)
	return err
}

func (s *SQL) TouchPAT(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pat SET last_used_at=? WHERE id=?`, NowTS(), id)
	return err
}
