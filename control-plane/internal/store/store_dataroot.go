package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

// A tenant slug and a default-tenant user key name sibling directories directly under
// the data root (<root>/<slug>/… and <root>/<user_key>), next to the CP's own entries
// (datalayout.Reserved). These errors are the store refusing a new tenant or a new
// default-tenant membership whose name would land on an entry that already belongs to
// someone else. Existing rows are never re-checked: a running deployment that already
// has such a name keeps working and is reported by DataRootCollisions instead.
var (
	ErrDataRootNameReserved = errors.New("the name is reserved for the control plane's own files under the data root")
	ErrDataRootNameTaken    = errors.New("the name is already used directly under the data root")
)

// defaultTenantMemberKeyExists reports whether any default-tenant membership, active or
// not, belongs to an identity whose user key equals name. An inactive membership still
// counts: its home stays on disk until somebody destroys it.
func (s *SQL) defaultTenantMemberKeyExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership m
		   JOIN identity i ON i.id = m.identity_id
		   JOIN tenant t ON t.id = m.tenant_id
		  WHERE t.slug = 'default' AND LOWER(i.user_key) = ?`, strings.ToLower(name)).Scan(&n)
	return n > 0, err
}

// checkTenantSlugFree refuses a slug for a NEW tenant.
func (s *SQL) checkTenantSlugFree(ctx context.Context, slug string) error {
	if datalayout.IsReserved(slug) {
		return fmt.Errorf("%w: tenant slug %q", ErrDataRootNameReserved, slug)
	}
	taken, err := s.defaultTenantMemberKeyExists(ctx, slug)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: tenant slug %q is a default-tenant member's home", ErrDataRootNameTaken, slug)
	}
	return nil
}

// checkDefaultMemberKeyFree refuses a NEW membership of identityID in tenantID when that
// tenant is the default one and the identity's user key, which becomes the home's
// directory name, is reserved or is another tenant's directory. Other tenants nest their
// homes under their slug, so their keys cannot collide at the root.
func (s *SQL) checkDefaultMemberKeyFree(ctx context.Context, identityID, tenantID string) error {
	var slug, key string
	if err := s.db.QueryRowContext(ctx, `SELECT slug FROM tenant WHERE id=?`, tenantID).Scan(&slug); err != nil {
		return err
	}
	if slug != "default" {
		return nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT user_key FROM identity WHERE id=?`, identityID).Scan(&key); err != nil {
		return err
	}
	if datalayout.IsReserved(key) {
		return fmt.Errorf("%w: user key %q", ErrDataRootNameReserved, key)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tenant WHERE LOWER(slug) = ?`, strings.ToLower(key)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: user key %q is a tenant's directory", ErrDataRootNameTaken, key)
	}
	return nil
}

// DataRootCollisions lists the stored names that already collide under the data root:
// non-default tenant slugs that are reserved or equal a default-tenant member's key, and
// default-tenant member keys that are reserved. Startup only logs them — refusing to
// boot would take a running deployment down for a state it may have lived with for
// months.
func (s *SQL) DataRootCollisions(ctx context.Context) ([]string, error) {
	var out []string
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tenants {
		if t.Slug == "default" {
			continue
		}
		if datalayout.IsReserved(t.Slug) {
			out = append(out, fmt.Sprintf("tenant %q uses a reserved name", t.Slug))
			continue
		}
		taken, err := s.defaultTenantMemberKeyExists(ctx, t.Slug)
		if err != nil {
			return nil, err
		}
		if taken {
			out = append(out, fmt.Sprintf("tenant %q has the same directory as the default-tenant member %q", t.Slug, t.Slug))
		}
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.user_key FROM membership m
		   JOIN identity i ON i.id = m.identity_id
		   JOIN tenant t ON t.id = m.tenant_id
		  WHERE t.slug = 'default' ORDER BY i.user_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		if datalayout.IsReserved(key) {
			out = append(out, fmt.Sprintf("default-tenant member %q uses a reserved name", key))
		}
	}
	return out, rows.Err()
}
