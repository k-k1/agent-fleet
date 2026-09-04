// system_tenant.go — reserved tenants: rows shaped like a tenant that are not a container
// for people.
//
// There is one so far: `af-golden`, used by the automatic golden-snapshot bake
// (golden_bake.go). Its `af-golden-seed` / `af-golden-probe` members create workspaces
// through the product's ordinary Start path — otherwise the baked golden would not be a
// copy of the home the product actually creates — so the tenant and its memberships exist
// as real rows.
//
// A reserved tenant is a container, not something to delete: the bake reuses it and throws
// away only the workspace, the home and the slot each time. So the right treatment is to
// hide it, never to remove it.
//
// The predicate lives in one place because it feeds three separate surfaces: dropping the
// tenant from listings (listTenants in tenants.go), refusing deletion (deleteTenant /
// deleteMembership) and folding its cost into shared infrastructure (cloudcost.go). Once
// the slug is inlined in several places, the next reserved tenant will be missed by one of
// the three.
package main

import "context"

// isSystemTenantSlug answers whether this tenant is a reserved one rather than a container
// for people.
func isSystemTenantSlug(slug string) bool {
	return slug == goldenTenantSlug
}

// systemTenantSlugs is every reserved tenant. A new one goes here and in
// isSystemTenantSlug.
func systemTenantSlugs() []string { return []string{goldenTenantSlug} }

// systemMembershipIDs returns the set of membership ids belonging to reserved tenants.
//
// It collects active and inactive ones alike. A reserved membership can be inactive
// between bakes (destroy removes the workspace but leaves the membership row), and missing
// those produces the hardest failure to notice: only the cost of a golden that happens to
// be inactive right now shows up in a human's column.
//
// A deployment with no reserved tenant at all (the norm outside ecs-ec2) gets an empty
// set. Errors are returned, but a caller may carry on with "no set means no folding" —
// this is not worth stopping cost ingestion for.
func (m *manager) systemMembershipIDs(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for _, slug := range systemTenantSlugs() {
		t, ok, err := m.store.GetTenantBySlug(ctx, slug)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		active, err := m.store.ListMembersByTenant(ctx, t.ID)
		if err != nil {
			return out, err
		}
		removed, err := m.store.ListRemovedMembersByTenant(ctx, t.ID)
		if err != nil {
			return out, err
		}
		for _, mi := range append(active, removed...) {
			out[mi.MembershipID] = true
		}
	}
	return out, nil
}
