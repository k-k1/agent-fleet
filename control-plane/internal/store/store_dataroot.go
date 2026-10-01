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
//
// The comparison is strings.EqualFold in Go, not LOWER() in SQL: SQLite's LOWER folds
// ASCII only while Postgres folds Unicode, so the same rows would collide on one dialect
// and not on the other (measured with a stored key spelled with U+212A KELVIN SIGN).
func (s *SQL) defaultTenantMemberKeyExists(ctx context.Context, name string) (bool, error) {
	keys, err := s.defaultTenantMemberKeys(ctx)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if strings.EqualFold(k, name) {
			return true, nil
		}
	}
	return false, nil
}

func (s *SQL) defaultTenantMemberKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.user_key FROM membership m
		   JOIN identity i ON i.id = m.identity_id
		   JOIN tenant t ON t.id = m.tenant_id
		  WHERE t.slug = 'default' ORDER BY i.user_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
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
	// The default tenant itself has no directory of its own (its homes are flat), so a
	// member keyed "default" collides with nothing.
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if t.Slug != "default" && strings.EqualFold(t.Slug, key) {
			return fmt.Errorf("%w: user key %q is a tenant's directory", ErrDataRootNameTaken, key)
		}
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
	keys, err := s.defaultTenantMemberKeys(ctx)
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
		for _, k := range keys {
			if strings.EqualFold(k, t.Slug) {
				out = append(out, fmt.Sprintf("tenant %q has the same directory as the default-tenant member %q", t.Slug, k))
			}
		}
	}
	for _, k := range keys {
		if datalayout.IsReserved(k) {
			out = append(out, fmt.Sprintf("default-tenant member %q uses a reserved name", k))
		}
	}
	return out, nil
}
