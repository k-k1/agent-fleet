package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
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

// defaultTenantSlug is the tenant EnsureDefaultTenant guarantees exists at every
// start (main.go), with id == slug. Since P7 it is also where the DEPLOYMENT's own
// sign-in methods live (docs/61 §61.17): the env providers are read as the default
// tenant's methods, so "the deployment's rules" and "this tenant's rules" stop being
// two different layers. ★ Only the DISPLAY and the RULES moved — provider ids keep
// their env shape (`google`, not `t:default:google`), because ten places branch on
// that shape (§61.17.3).
const defaultTenantSlug = "default"

// The kinds a row can be built into (docs/61 §61.15). "" is read as oidc: rows
// written before 0041 predate the column, and P4 had only one kind.
const (
	tenantIdPKindOIDC   = "oidc"
	tenantIdPKindGitHub = "github"
)

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
	ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error)
}

// tenantProviderSnapshot is one consistent read of every ACTIVE tenant provider.
//
// ★ loginProvider, not *oidcProvider: since §61.15 a row can also be built into the
// GitHub adapter, which is not an OIDC client at all (it reads the REST API).
type tenantProviderSnapshot struct {
	byID   map[string]loginProvider
	bySlug map[string][]loginProvider // login-page order, per tenant
}

// builtProvider caches the constructed provider so an unchanged row keeps its OIDC
// discovery cache instead of re-fetching .well-known every refresh — and, for a
// GitHub row, its org-membership cache, which is what stands between a refresh and
// making everyone sign in again (oauth_github.go: an empty cache answers reauth).
type builtProvider struct {
	updatedAt string
	prov      loginProvider
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

	rows, tenants, err := r.store.ListActiveTenantIdPs(ctx)
	if err != nil {
		log.Printf("tenant idp: %v (using the previous snapshot)", err)
		r.mu.Lock()
		s := r.snap
		r.mu.Unlock()
		return s
	}

	snap := &tenantProviderSnapshot{
		byID:   map[string]loginProvider{},
		bySlug: map[string][]loginProvider{},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := make(map[string]builtProvider, len(rows))
	for _, row := range rows {
		tn := tenants[row.TenantID]
		if tn.Slug == "" {
			continue // tenant vanished between the join and here
		}
		id := tenantProviderID(tn.Slug, row.Name)
		p, ok := r.built[id]
		if !ok || p.updatedAt != row.UpdatedAt {
			secret, err := r.secret(ctx, row.SecretEnc, row.KeyRef)
			if err == nil {
				var prov loginProvider
				prov, err = buildTenantProvider(row, tn, secret)
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
		snap.bySlug[tn.Slug] = append(snap.bySlug[tn.Slug], p.prov)
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
	return append([]loginProvider(nil), snap.bySlug[slug]...)
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
//
// ★ kind picks the adapter (docs/61 §61.15 + 決定 34). This function is the RUNTIME
// half of the same rules validateTenantIdPBody applies on save, and the two must
// stay in step: an approved row that only the API accepts would save cleanly and
// then disappear from the login page with a log line nobody is watching.
func buildTenantProvider(row TenantIdP, tn TenantRef, secret string) (loginProvider, error) {
	if !validTenantIdPName(row.Name) {
		return nil, fmt.Errorf("invalid provider name %q", row.Name)
	}
	if row.Kind == tenantIdPKindGitHub {
		return newTenantGitHubProvider(row, tn, secret)
	}
	if row.Kind != "" && row.Kind != tenantIdPKindOIDC {
		return nil, fmt.Errorf("unknown sign-in method kind %q", row.Kind)
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
	// ★ The runtime half of the link_claim whitelist. Validated here as well as in
	// validateTenantIdPBody because those are two different moments: a row saved
	// before the list changed, or written by an older binary, must not be built into
	// a provider that joins accounts on a claim this deployment never allowed.
	if row.LinkClaim != "" && !tenantLinkClaims[row.LinkClaim] {
		return nil, fmt.Errorf("link_claim %q is not one this deployment allows a tenant to name (%s)",
			row.LinkClaim, strings.Join(tenantLinkClaimList(), ", "))
	}
	id := tenantProviderID(tn.Slug, row.Name)
	labelJA, labelEN := row.LabelJA, row.LabelEN
	if labelJA == "" {
		labelJA = tenantLabelSuffix(defaultProviderLabel(row.Name, "ja"), tn, "ja")
	}
	if labelEN == "" {
		labelEN = tenantLabelSuffix(defaultProviderLabel(row.Name, "en"), tn, "en")
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
		linkClaim:    row.LinkClaim,
	}, nil
}

// tenantLinkClaims is the CLOSED set of claims a tenant row may name for rule 1.5's
// second key (docs/61 §61.15.10 + 決定 38).
//
// ★ It is a whitelist and not a validity check, and the reason is the whole point of
// 決定 32. `oid` is a per-directory object id: two app registrations in one Entra
// tenant report the same one for the same person, and nobody can choose what it says.
// `email` / `upn` / `preferred_username` are the opposite — they are asserted, and a
// tenant that could name one of them would have built an email join INSIDE a shared
// realm, reaching accounts created by another authority. That is exactly the takeover
// rule 2' exists to refuse, arriving through a different door.
//
// Adding to this list is a decision about which claims an IdP does not let its
// tenants choose, not a convenience.
var tenantLinkClaims = map[string]bool{"oid": true}

// tenantLinkClaimList is the same set, sorted, for error messages and the API.
func tenantLinkClaimList() []string {
	out := make([]string, 0, len(tenantLinkClaims))
	for c := range tenantLinkClaims {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// tenantLabelSuffix names the company inside a tenant row's DEFAULT button label
// (docs/61 §61.15.10).
//
// The problem it solves is only visible on /login/<slug>, where the deployment's own
// buttons and the tenant's own rows are rendered side by side: a tenant row named
// "github" generated "GitHub でサインイン", which is exactly the env GitHub button's
// text. Two identical buttons that send you to two different OAuth apps is a place
// people get stuck, and the stuck person cannot tell them apart by looking.
//
// ★ It only ever touches the GENERATED label. A row that filled in label_ja /
// label_en wrote what its administrator wants on the button, and appending to that
// would override a deliberate choice — so the caller applies this to the fallback
// only, and the existing precedence (row label > generated) is unchanged.
//
// The display name is preferred over the slug because it is what a human calls that
// company; the slug is the fallback for a tenant that never set one.
func tenantLabelSuffix(base string, tn TenantRef, lang string) string {
	who := strings.TrimSpace(tn.Name)
	if who == "" {
		who = strings.TrimSpace(tn.Slug)
	}
	if who == "" || base == "" {
		return base
	}
	if lang == "en" {
		return base + " (" + who + ")"
	}
	return base + "（" + who + "）"
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
