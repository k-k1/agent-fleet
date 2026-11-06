package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
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
	// RevokeBearersByIdentity ends every live PAT and MCP OAuth grant of one person (the
	// offboarding revoke for someone who may no longer sign in).
	RevokeBearersByIdentity(ctx context.Context, identityID, reason string, before int64) error
}

// --- Personal Access Tokens (docs/decisions/0006, P3-6) ---

// ErrMembershipInactive is returned when a credential is issued against a membership that is
// no longer active.
var ErrMembershipInactive = errors.New("membership is not active")

// lockActiveMembershipTx takes the membership row's write lock (PostgreSQL row lock, SQLite write
// lock) and fails unless it is active. SetMembershipStatus writes the same row first, so an
// issuance and a deactivation are serialized: either the issuance commits before the deactivation
// and the revoke that follows sees the new credential, or it runs after and is refused here. A
// plain read of the status would leave the window between "was active" and the insert open.
func lockActiveMembershipTx(ctx context.Context, tx *sqlTx, membershipID string) error {
	if membershipID == "" {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE membership SET status=status WHERE id=? AND status='active'`, membershipID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		// Refuse a membership that exists and is not active. One that does not exist at all is
		// left to the insert's own constraints (the PAT table has a foreign key to it).
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM membership WHERE id=?`, membershipID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return ErrMembershipInactive
	}
	return nil
}

func (s *SQL) CreatePAT(ctx context.Context, p PAT, tokenHash string) error {
	if p.Resource == "" {
		p.Resource = PATResourceMCP
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockActiveMembershipTx(ctx, tx, p.MembershipID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pat(id, identity_id, membership_id, token_hash, scope, name, created_at, expires_at, resource)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.IdentityID, nullable(p.MembershipID), tokenHash, p.Scope, p.Name, p.CreatedAt, nullable(p.ExpiresAt), p.Resource); err != nil {
		return err
	}
	return tx.Commit()
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

// revokeBearersTx revokes the live PATs and MCP OAuth grants selected by column (identity_id
// or membership_id) = id, and deletes the grants' tokens. It is a transaction step so that
// deactivating a membership cannot leave its credentials alive after a crash.
func revokeBearersTx(ctx context.Context, tx *sqlTx, column, id, reason string) error {
	now := NowTS()
	if _, err := tx.ExecContext(ctx,
		`UPDATE pat SET revoked_at=? WHERE `+column+`=? AND revoked_at IS NULL`, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM mcp_oauth_token WHERE grant_id IN (SELECT id FROM mcp_oauth_grant WHERE `+column+`=? AND revoked_at=0)`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE mcp_oauth_grant SET revoked_at=?, revoked_reason=? WHERE `+column+`=? AND revoked_at=0`,
		time.Now().Unix(), reason, id)
	return err
}

// RevokeBearersByIdentity revokes the person's credentials issued up to and including second `before` (Unix
// seconds). The caller passes the moment its allowed/denied judgment began: a token or
// grant minted after that was never part of the judgment, so a re-login that completed in between
// keeps what it just got, and the next use re-judges.
func (s *SQL) RevokeBearersByIdentity(ctx context.Context, identityID, reason string, before int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cut := time.Unix(before, 0).UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`UPDATE pat SET revoked_at=? WHERE identity_id=? AND revoked_at IS NULL AND created_at <= ?`, NowTS(), identityID, cut); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM mcp_oauth_token WHERE grant_id IN (SELECT id FROM mcp_oauth_grant WHERE identity_id=? AND revoked_at=0 AND created_at <= ?)`, identityID, before); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE mcp_oauth_grant SET revoked_at=?, revoked_reason=? WHERE identity_id=? AND revoked_at=0 AND created_at <= ?`,
		time.Now().Unix(), reason, identityID, before); err != nil {
		return err
	}
	return tx.Commit()
}
