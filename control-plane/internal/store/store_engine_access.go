package store

import (
	"context"
	"fmt"
)

// Engine roles a tenant_admin can restrict per member (#1215). They are the words the
// tenant limits already use (allow_engine_llm / allow_engine_image), not the gateway's
// api names, because this is the vocabulary the admin screen and the stored rows share.
const (
	EngineAccessLLM   = "llm"
	EngineAccessImage = "image"
)

// ValidEngineAccessRole reports whether role is one of the restrictable roles.
func ValidEngineAccessRole(role string) bool {
	return role == EngineAccessLLM || role == EngineAccessImage
}

// EngineAccess is one tenant's per-member restriction of the self-hosted engine roles.
// It narrows the super_admin's tenant gate and can never widen it.
type EngineAccess struct {
	// MembersOnly holds the roles restricted to granted members. A role absent from it
	// is open to every member of the tenant.
	MembersOnly map[string]bool
	// Grants is membership id -> role -> granted. Kept while a role is open to everyone
	// so switching the restriction back on restores the same list.
	Grants map[string]map[string]bool
}

// Allows reports whether membershipID may use role under this restriction alone. A
// tenant_admin is not implicitly granted: the restriction names seats, not roles.
func (a EngineAccess) Allows(membershipID, role string) bool {
	if !a.MembersOnly[role] {
		return true
	}
	return a.Grants[membershipID][role]
}

// EngineAccessStore holds the tenant_admin's per-member engine restriction (#1215).
type EngineAccessStore interface {
	GetEngineAccess(ctx context.Context, tenantID string) (EngineAccess, error)
	SetEngineMembersOnly(ctx context.Context, tenantID, role string, on bool) error
	// SetEngineGrant grants or revokes one role for one membership. The caller has
	// checked that the membership belongs to tenantID.
	SetEngineGrant(ctx context.Context, tenantID, membershipID, role string, on bool) error
}

func (s *SQL) GetEngineAccess(ctx context.Context, tenantID string) (EngineAccess, error) {
	out := EngineAccess{MembersOnly: map[string]bool{}, Grants: map[string]map[string]bool{}}
	rows, err := s.db.QueryContext(ctx, `SELECT role FROM engine_access_policy WHERE tenant_id=?`, tenantID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return out, err
		}
		out.MembersOnly[role] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT membership_id, role FROM engine_access_grant WHERE tenant_id=?`, tenantID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid, role string
		if err := rows.Scan(&mid, &role); err != nil {
			return out, err
		}
		if out.Grants[mid] == nil {
			out.Grants[mid] = map[string]bool{}
		}
		out.Grants[mid][role] = true
	}
	return out, rows.Err()
}

func (s *SQL) SetEngineMembersOnly(ctx context.Context, tenantID, role string, on bool) error {
	if !ValidEngineAccessRole(role) {
		return fmt.Errorf("unknown engine access role %q", role)
	}
	if !on {
		_, err := s.db.ExecContext(ctx, `DELETE FROM engine_access_policy WHERE tenant_id=? AND role=?`, tenantID, role)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_access_policy(tenant_id, role, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(tenant_id, role) DO NOTHING`, tenantID, role, NowTS())
	return err
}

func (s *SQL) SetEngineGrant(ctx context.Context, tenantID, membershipID, role string, on bool) error {
	if !ValidEngineAccessRole(role) {
		return fmt.Errorf("unknown engine access role %q", role)
	}
	if !on {
		_, err := s.db.ExecContext(ctx, `DELETE FROM engine_access_grant WHERE membership_id=? AND role=?`, membershipID, role)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_access_grant(membership_id, tenant_id, role, created_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(membership_id, role) DO NOTHING`, membershipID, tenantID, role, NowTS())
	return err
}
