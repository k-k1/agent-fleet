package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Tenant-defined login providers (docs/61 §61.11 + ADR0043 決定 29-33).
//
// P0 built the provider set ONCE, at startup, from the environment. P4 cannot: a
// subsidiary's IdP is created by its own administrator and activated by the
// operator, and both have to take effect without restarting CP — that is the whole
// reason the definition moved out of env (決定 29). So this file holds a second,
// RUNTIME set of providers, assembled from the database behind the same short-TTL
// cache the per-tenant login rules use (tenant_login.go).
//
// ★ Why here and not in config.providers: config is copied BY VALUE into buildMux
// and every handler, so a set stored there can never change after startup. The
// registry therefore hangs off the manager (a pointer), exactly like tenantLogin,
// and config reaches it through c.mgr. env-defined providers keep their startup-fixed
// home in config.providers; only the database-derived ones are dynamic.
//
// ★ Namespace: an env provider is "entra", a tenant-defined one is
// "t:<tenant-slug>:<name>". Without the split, a tenant could create a row named
// "google" and shadow the deployment's Google button (決定 33). The prefix is also
// how every gate downstream recognises a tenant-defined session — sessionClaims.prov
// carries this id, so resolveFull only has to look at the string.

// tenantProviderPrefix marks a provider id as database-derived.
const tenantProviderPrefix = "t:"

// tenantProviderID builds the deployment-wide id of a tenant's provider.
func tenantProviderID(slug, name string) string {
	return tenantProviderPrefix + slug + ":" + name
}

// parseTenantProviderID splits a tenant provider id back into (slug, name). ok is
// false for an env-defined id, which is what every "is this tenant-defined?" caller
// actually asks.
func parseTenantProviderID(id string) (slug, name string, ok bool) {
	if !strings.HasPrefix(id, tenantProviderPrefix) {
		return "", "", false
	}
	rest := id[len(tenantProviderPrefix):]
	i := strings.IndexByte(rest, ':')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// isTenantProviderID reports a database-derived provider id.
func isTenantProviderID(id string) bool {
	_, _, ok := parseTenantProviderID(id)
	return ok
}

// validTenantIdPName keeps the per-tenant half of the id to the same character set
// env ids use. ★ It is deliberately a SEPARATE function from validProviderID rather
// than a relaxation of it: validProviderID rejects ":" and must keep doing so, or an
// env provider could be named into the tenant namespace.
func validTenantIdPName(name string) bool { return validProviderID(name) }

// --- the runtime registry ---------------------------------------------------

// tenantIdPStoreView is the narrow store view the registry needs.
type tenantIdPStoreView interface {
	ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]string, error)
}

// tenantProviderSnapshot is one consistent read of every ACTIVE tenant provider.
type tenantProviderSnapshot struct {
	byID   map[string]*oidcProvider
	bySlug map[string][]*oidcProvider // login-page order, per tenant
	// tenantOf maps a provider id to the tenant that owns it, so the tenant gate can
	// pin a session without parsing the slug back into a lookup.
	tenantOf map[string]string
}

// builtProvider caches the constructed provider so an unchanged row keeps its OIDC
// discovery cache instead of re-fetching .well-known every refresh.
type builtProvider struct {
	updatedAt string
	prov      *oidcProvider
}

type tenantIdPRegistry struct {
	store  tenantIdPStoreView
	secret func(ctx context.Context, enc, keyRef string) (string, error)
	ttl    time.Duration

	mu    sync.Mutex
	snap  *tenantProviderSnapshot
	exp   time.Time
	built map[string]builtProvider
	// warned remembers which rows we already complained about, so a permanently
	// broken row logs once rather than every 30 seconds.
	warned map[string]string
}

func newTenantIdPRegistry(st tenantIdPStoreView, secret func(context.Context, string, string) (string, error)) *tenantIdPRegistry {
	return &tenantIdPRegistry{
		store: st, secret: secret, ttl: tenantRuleTTL,
		built: map[string]builtProvider{}, warned: map[string]string{},
	}
}

// invalidate drops the cache. Every admin write that can change which providers
// exist — create, edit, approve, suspend, delete — calls it, so approving a
// subsidiary's IdP puts its button on the login page immediately (決定 29: no
// restart), and suspending one takes the button away just as fast.
func (r *tenantIdPRegistry) invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snap, r.exp = nil, time.Time{}
	r.mu.Unlock()
}

// snapshot returns the active tenant providers, refreshing when stale.
//
// The failure policy is tenant_login.go's, for the same reason: a database blip
// must not silently log every subsidiary out (their sessions are re-checked on
// every request through sessionAllowed), so a failed refresh keeps serving the
// previous snapshot. With no snapshot at all the answer is "no tenant providers",
// which is fail-closed — nothing is admitted on a definition that could not be read.
func (r *tenantIdPRegistry) snapshot(ctx context.Context) *tenantProviderSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.snap != nil && time.Now().Before(r.exp) {
		s := r.snap
		r.mu.Unlock()
		return s
	}
	r.mu.Unlock()

	rows, slugs, err := r.store.ListActiveTenantIdPs(ctx)
	if err != nil {
		log.Printf("tenant idp: %v (using the previous snapshot)", err)
		r.mu.Lock()
		s := r.snap
		r.mu.Unlock()
		return s
	}

	snap := &tenantProviderSnapshot{
		byID:     map[string]*oidcProvider{},
		bySlug:   map[string][]*oidcProvider{},
		tenantOf: map[string]string{},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := make(map[string]builtProvider, len(rows))
	for _, row := range rows {
		slug := slugs[row.TenantID]
		if slug == "" {
			continue // tenant vanished between the join and here
		}
		id := tenantProviderID(slug, row.Name)
		p, ok := r.built[id]
		if !ok || p.updatedAt != row.UpdatedAt {
			secret, err := r.secret(ctx, row.SecretEnc, row.KeyRef)
			if err == nil {
				var prov *oidcProvider
				prov, err = buildTenantProvider(row, slug, secret)
				if err == nil {
					p = builtProvider{updatedAt: row.UpdatedAt, prov: prov}
				}
			}
			if err != nil {
				// One broken subsidiary must never take the others down (決定 11), so
				// the row is dropped rather than fatal — but it is dropped LOUDLY: from
				// the tenant's point of view an approved IdP that shows no button is
				// otherwise indistinguishable from one that was never approved.
				if r.warned[id] != row.UpdatedAt {
					r.warned[id] = row.UpdatedAt
					log.Printf("WARNING: tenant login provider %q is unusable and its button is hidden: %v", id, err)
				}
				continue
			}
		}
		keep[id] = p
		snap.byID[id] = p.prov
		snap.bySlug[slug] = append(snap.bySlug[slug], p.prov)
		snap.tenantOf[id] = row.TenantID
	}
	r.built = keep
	r.snap, r.exp = snap, time.Now().Add(r.ttl)
	return snap
}

// providerFor resolves an active tenant provider by id (callback / session re-check).
func (r *tenantIdPRegistry) providerFor(ctx context.Context, id string) loginProvider {
	if !isTenantProviderID(id) {
		return nil
	}
	snap := r.snapshot(ctx)
	if snap == nil {
		return nil
	}
	if p, ok := snap.byID[id]; ok {
		return p
	}
	return nil
}

// providersForSlug returns a tenant's active providers, for its login page.
// ★ Only /login/<slug> ever calls this. The generic /login must not show a
// subsidiary's button: the full set of buttons would be a directory of the group's
// companies, readable without signing in (決定 32-4).
func (r *tenantIdPRegistry) providersForSlug(ctx context.Context, slug string) []loginProvider {
	if r == nil || slug == "" {
		return nil
	}
	snap := r.snapshot(ctx)
	if snap == nil {
		return nil
	}
	out := make([]loginProvider, 0, len(snap.bySlug[slug]))
	for _, p := range snap.bySlug[slug] {
		out = append(out, p)
	}
	return out
}

// tenantIDFor returns the tenant that owns an active provider id.
func (r *tenantIdPRegistry) tenantIDFor(ctx context.Context, id string) (string, bool) {
	if r == nil || !isTenantProviderID(id) {
		return "", false
	}
	snap := r.snapshot(ctx)
	if snap == nil {
		return "", false
	}
	t, ok := snap.tenantOf[id]
	return t, ok
}

// --- building one provider from its row --------------------------------------

// buildTenantProvider turns a stored row into the same generic OIDC client env
// providers use (oauth_oidc.go). The differences are all in the authorization
// terms, and each is a decision:
//
//	deployAllowed = nil   the deployment-wide allowlist is NOT a fallback. A
//	                      subsidiary's IdP may admit the subsidiary's people, never
//	                      "everyone this deployment would have admitted" (決定 32-3).
//	dbAllowed     = nil   nor is the deployment-wide roster term (§61.9.6): holding a
//	                      membership of tenant A is not a reason to be let in by
//	                      tenant B's issuer.
//	allowDomains          required, and therefore always the effective gate. See
//	                      validateTenantIdPBody for why it cannot be empty.
//
// allowed_tids keeps its P0 meaning and is checked inside Exchange, on a different
// axis (the token's tenant), so it stays AND-ed.
func buildTenantProvider(row TenantIdP, slug, secret string) (*oidcProvider, error) {
	if !validTenantIdPName(row.Name) {
		return nil, fmt.Errorf("invalid provider name %q", row.Name)
	}
	if row.ClientID == "" || secret == "" {
		return nil, errors.New("client_id / client_secret are required")
	}
	if !validIssuerURL(row.Issuer) {
		return nil, fmt.Errorf("issuer %q is not an https URL", row.Issuer)
	}
	tids := emailSet(row.AllowedTIDs)
	if multiTenantIssuer(row.Issuer) && len(tids) == 0 {
		return nil, fmt.Errorf("issuer %q is a multi-tenant endpoint and no allowed_tids are set", row.Issuer)
	}
	switch row.Trust {
	case trustEmailVerified, trustIssuer:
	default:
		return nil, fmt.Errorf("trust must be %q or %q", trustEmailVerified, trustIssuer)
	}
	domains := domainSet(row.AllowedDomains)
	if len(domains) == 0 {
		return nil, errors.New("allowed_domains is empty, which would admit every address this issuer asserts")
	}
	id := tenantProviderID(slug, row.Name)
	labelJA, labelEN := row.LabelJA, row.LabelEN
	if labelJA == "" {
		labelJA = defaultProviderLabel(row.Name, "ja")
	}
	if labelEN == "" {
		labelEN = defaultProviderLabel(row.Name, "en")
	}
	return &oidcProvider{
		id:           id,
		labelJA:      labelJA,
		labelEN:      labelEN,
		issuer:       row.Issuer,
		clientID:     row.ClientID,
		clientSecret: secret,
		trust:        row.Trust,
		scope:        "openid email profile",
		prompt:       "select_account",
		allowedTIDs:  tids,
		allowDomains: domains,
	}, nil
}

// --- secret sealing (docs/61 §61.11.4 + 決定 33) ------------------------------

// sealTenantSecret seals a client_secret with the tenant key, exactly as
// mcp_server.sealHeaders does for header values: AES-256-GCM through the custodian,
// with the key reference as AAD so the ciphertext is bound to the tenant.
//
// With no master key configured (dev / a single node without one) the value is
// stored as plaintext with an empty key_ref — the same degradation as everywhere
// else in CP, rather than refusing to work.
func (m *manager) sealTenantSecret(ctx context.Context, tenantID, secret string) (enc, keyRef string, err error) {
	if secret == "" {
		return "", "", nil
	}
	if len(m.master32) == 0 || m.custodian == nil {
		return secret, "", nil
	}
	ct, err := m.custodian.Wrap(ctx, tenantID, []byte(secret))
	if err != nil {
		return "", "", err
	}
	return ct, tenantID, nil
}

// openTenantSecret reverses sealTenantSecret. An unreadable value is an ERROR, never
// an empty secret: a token exchange with an empty client_secret fails at the IdP
// with a message nobody can trace back to a key change (docs/61 §61.11.4).
func (m *manager) openTenantSecret(ctx context.Context, enc, keyRef string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if keyRef == "" {
		return enc, nil
	}
	if m.custodian == nil {
		return "", errors.New("the client secret is sealed with a tenant key but no key custodian is configured")
	}
	b, err := m.custodian.Unwrap(ctx, keyRef, enc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
