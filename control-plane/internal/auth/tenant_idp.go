package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Tenant-defined login providers (docs/log/61 §61.11 + ADR0043 decisions 29-33).
//
// P0 built the provider set ONCE, at startup, from the environment. P4 cannot: a
// subsidiary's IdP is created by its own administrator and activated by the
// operator, and both have to take effect without restarting CP — that is the whole
// reason the definition moved out of env (decision 29). So this file holds a second,
// RUNTIME set of providers, assembled from the database behind the same short-TTL
// cache the per-tenant login rules use (tenant_login.go).
//
// Why here and not in config.providers: config is copied BY VALUE into buildMux
// and every handler, so a set stored there can never change after startup. The
// registry therefore hangs off the manager (a pointer), exactly like tenantLogin,
// and config reaches it through c.mgr. env-defined providers keep their startup-fixed
// home in config.providers; only the database-derived ones are dynamic.
//
// Namespace: an env provider is "entra", a tenant-defined one is
// "t:<tenant-slug>:<name>". Without the split, a tenant could create a row named
// "google" and shadow the deployment's Google button (decision 33). The prefix is also
// how every gate downstream recognises a tenant-defined session — sessionClaims.prov
// carries this id, so resolveFull only has to look at the string.

// TenantProviderPrefix marks a provider id as database-derived.
const TenantProviderPrefix = "t:"

// DefaultTenantSlug is the tenant EnsureDefaultTenant guarantees exists at every
// start (main.go), with id == slug. Since P7 it is also where the DEPLOYMENT's own
// sign-in methods live (docs/log/61 §61.17): the env providers are read as the default
// tenant's methods, so "the deployment's rules" and "this tenant's rules" stop being
// two different layers. Only the DISPLAY and the RULES moved — provider ids keep
// their env shape (`google`, not `t:default:google`), because ten places branch on
// that shape (§61.17.3).
const DefaultTenantSlug = "default"

// The kinds a row can be built into (docs/log/61 §61.15). "" is read as oidc: rows
// written before 0041 predate the column, and P4 had only one kind.
const (
	TenantIdPKindOIDC   = "oidc"
	TenantIdPKindGitHub = "github"
)

// TenantProviderID builds the deployment-wide id of a tenant's provider.
func TenantProviderID(slug, name string) string {
	return TenantProviderPrefix + slug + ":" + name
}

// ParseTenantProviderID splits a tenant provider id back into (slug, name). ok is
// false for an env-defined id, which is what every "is this tenant-defined?" caller
// actually asks.
func ParseTenantProviderID(id string) (slug, name string, ok bool) {
	if !strings.HasPrefix(id, TenantProviderPrefix) {
		return "", "", false
	}
	rest := id[len(TenantProviderPrefix):]
	i := strings.IndexByte(rest, ':')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// IsTenantProviderID reports a database-derived provider id.
func IsTenantProviderID(id string) bool {
	_, _, ok := ParseTenantProviderID(id)
	return ok
}

// ValidTenantIdPName keeps the per-tenant half of the id to the same character set
// env ids use. It is deliberately a SEPARATE function from ValidProviderID rather
// than a relaxation of it: ValidProviderID rejects ":" and must keep doing so, or an
// env provider could be named into the tenant namespace.
func ValidTenantIdPName(name string) bool { return ValidProviderID(name) }

// --- the runtime registry ---------------------------------------------------

// TenantIdPStoreView is the narrow store view the registry needs.
type TenantIdPStoreView interface {
	ListActiveTenantIdPs(ctx context.Context) ([]store.TenantIdP, map[string]store.TenantRef, error)
}

// tenantProviderSnapshot is one consistent read of every ACTIVE tenant provider.
//
// LoginProvider, not *OIDCProvider: since §61.15 a row can also be built into the
// GitHub adapter, which is not an OIDC client at all (it reads the REST API).
type tenantProviderSnapshot struct {
	byID   map[string]LoginProvider
	bySlug map[string][]LoginProvider // login-page order, per tenant
}

// builtProvider caches the constructed provider so an unchanged row keeps its OIDC
// discovery cache instead of re-fetching .well-known every refresh — and, for a
// GitHub row, its org-membership cache, which is what stands between a refresh and
// making everyone sign in again (oauth_github.go: an empty cache answers reauth).
type builtProvider struct {
	updatedAt string
	prov      LoginProvider
}

type TenantIdPRegistry struct {
	store  TenantIdPStoreView
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

// TenantRuleTTL is short on purpose: it bounds how long a rule change takes to
// reach a deployment whose admin API is served by another replica (the drop on
// every admin write only reaches this process).
//
// One value serves both caches — this registry and the tenant login-rule cache
// in control-plane/tenant_login.go, which reads it back directly.
// They answer the same operational question ("how long until an admin change is
// live everywhere"), so they must not be allowed to drift apart.
const TenantRuleTTL = 30 * time.Second

func NewTenantIdPRegistry(st TenantIdPStoreView, secret func(context.Context, string, string) (string, error)) *TenantIdPRegistry {
	return &TenantIdPRegistry{
		store: st, secret: secret, ttl: TenantRuleTTL,
		built: map[string]builtProvider{}, warned: map[string]string{},
	}
}

// invalidate drops the cache. Every admin write that can change which providers
// exist — create, edit, approve, suspend, delete — calls it, so approving a
// subsidiary's IdP puts its button on the login page immediately (decision 29: no
// restart), and suspending one takes the button away just as fast.
func (r *TenantIdPRegistry) Invalidate() {
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
func (r *TenantIdPRegistry) snapshot(ctx context.Context) *tenantProviderSnapshot {
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
		byID:   map[string]LoginProvider{},
		bySlug: map[string][]LoginProvider{},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := make(map[string]builtProvider, len(rows))
	for _, row := range rows {
		tn := tenants[row.TenantID]
		if tn.Slug == "" {
			continue // tenant vanished between the join and here
		}
		id := TenantProviderID(tn.Slug, row.Name)
		p, ok := r.built[id]
		if !ok || p.updatedAt != row.UpdatedAt {
			secret, err := r.secret(ctx, row.SecretEnc, row.KeyRef)
			if err == nil {
				var prov LoginProvider
				prov, err = BuildTenantProvider(row, tn, secret)
				if err == nil {
					p = builtProvider{updatedAt: row.UpdatedAt, prov: prov}
				}
			}
			if err != nil {
				// One broken subsidiary must never take the others down (decision 11), so
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
func (r *TenantIdPRegistry) ProviderFor(ctx context.Context, id string) LoginProvider {
	if !IsTenantProviderID(id) {
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
// Only /login/<slug> ever calls this. The generic /login must not show a subsidiary's
// button: the full set of buttons would be a directory of the group's companies,
// readable without signing in (decision 32-4).
func (r *TenantIdPRegistry) ProvidersForSlug(ctx context.Context, slug string) []LoginProvider {
	if r == nil || slug == "" {
		return nil
	}
	snap := r.snapshot(ctx)
	if snap == nil {
		return nil
	}
	return append([]LoginProvider(nil), snap.bySlug[slug]...)
}

// --- building one provider from its row --------------------------------------

// BuildTenantProvider turns a stored row into the same generic OIDC client env
// providers use (oauth_oidc.go). The differences are all in the authorization
// terms, and each is a decision:
//
//	deployAllowed = nil   the deployment-wide allowlist is NOT a fallback. A
//	                      subsidiary's IdP may admit the subsidiary's people, never
//	                      "everyone this deployment would have admitted" (decision 32-3).
//	dbAllowed     = nil   nor is the deployment-wide roster term (§61.9.6): holding a
//	                      membership of tenant A is not a reason to be let in by
//	                      tenant B's issuer.
//	allowDomains          required, and therefore always the effective gate. See
//	                      validateTenantIdPBody for why it cannot be empty.
//
// allowed_tids keeps its P0 meaning and is checked inside Exchange, on a different
// axis (the token's tenant), so it stays AND-ed.
//
// kind picks the adapter (docs/log/61 §61.15 + decision 34). This function is the RUNTIME
// half of the same rules validateTenantIdPBody applies on save, and the two must
// stay in step: an approved row that only the API accepts would save cleanly and
// then disappear from the login page with a log line nobody is watching.
func BuildTenantProvider(row store.TenantIdP, tn store.TenantRef, secret string) (LoginProvider, error) {
	if !ValidTenantIdPName(row.Name) {
		return nil, fmt.Errorf("invalid provider name %q", row.Name)
	}
	if row.Kind == TenantIdPKindGitHub {
		return newTenantGitHubProvider(row, tn, secret)
	}
	if row.Kind != "" && row.Kind != TenantIdPKindOIDC {
		return nil, fmt.Errorf("unknown sign-in method kind %q", row.Kind)
	}
	if row.ClientID == "" || secret == "" {
		return nil, errors.New("client_id / client_secret are required")
	}
	if !ValidIssuerURL(row.Issuer) {
		return nil, fmt.Errorf("issuer %q is not an https URL", row.Issuer)
	}
	tids := envx.EmailSet(row.AllowedTIDs)
	if MultiTenantIssuer(row.Issuer) && len(tids) == 0 {
		return nil, fmt.Errorf("issuer %q is a multi-tenant endpoint and no allowed_tids are set", row.Issuer)
	}
	switch row.Trust {
	case TrustEmailVerified, TrustIssuer:
	default:
		return nil, fmt.Errorf("trust must be %q or %q", TrustEmailVerified, TrustIssuer)
	}
	domains := envx.DomainSet(row.AllowedDomains)
	if len(domains) == 0 {
		return nil, errors.New("allowed_domains is empty, which would admit every address this issuer asserts")
	}
	// The runtime half of the link_claim whitelist. Validated here as well as in
	// validateTenantIdPBody because those are two different moments: a row saved
	// before the list changed, or written by an older binary, must not be built into
	// a provider that joins accounts on a claim this deployment never allowed.
	if row.LinkClaim != "" && !TenantLinkClaims[row.LinkClaim] {
		return nil, fmt.Errorf("link_claim %q is not one this deployment allows a tenant to name (%s)",
			row.LinkClaim, strings.Join(TenantLinkClaimList(), ", "))
	}
	id := TenantProviderID(tn.Slug, row.Name)
	labelJA, labelEN := row.LabelJA, row.LabelEN
	if labelJA == "" {
		labelJA = TenantLabelSuffix(DefaultProviderLabel(row.Name, "ja"), tn, "ja")
	}
	if labelEN == "" {
		labelEN = TenantLabelSuffix(DefaultProviderLabel(row.Name, "en"), tn, "en")
	}
	return &OIDCProvider{
		ProviderID:   id,
		LabelJA:      labelJA,
		LabelEN:      labelEN,
		Issuer:       row.Issuer,
		ClientID:     row.ClientID,
		ClientSecret: secret,
		Trust:        row.Trust,
		Scope:        "openid email profile",
		Prompt:       "select_account",
		AllowedTIDs:  tids,
		AllowDomains: domains,
		LinkClaim:    row.LinkClaim,
	}, nil
}

// TenantLinkClaims is the CLOSED set of claims a tenant row may name for rule 1.5's
// second key (docs/log/61 §61.15.10 + decision 38).
//
// It is a whitelist and not a validity check, and the reason is the whole point of
// decision 32. `oid` is a per-directory object id: two app registrations in one Entra
// tenant report the same one for the same person, and nobody can choose what it says.
// `email` / `upn` / `preferred_username` are the opposite — they are asserted, and a
// tenant that could name one of them would have built an email join INSIDE a shared
// realm, reaching accounts created by another authority. That is exactly the takeover
// rule 2' exists to refuse, arriving through a different door.
//
// Adding to this list is a decision about which claims an IdP does not let its
// tenants choose, not a convenience.
var TenantLinkClaims = map[string]bool{"oid": true}

// TenantLinkClaimList is the same set, sorted, for error messages and the API.
func TenantLinkClaimList() []string {
	out := make([]string, 0, len(TenantLinkClaims))
	for c := range TenantLinkClaims {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// TenantLabelSuffix names the company inside a tenant row's DEFAULT button label
// (docs/log/61 §61.15.10).
//
// The problem it solves is only visible on /login/<slug>, where the deployment's own
// buttons and the tenant's own rows are rendered side by side: a tenant row named
// "github" generated exactly the env GitHub button's own "sign in with GitHub" text.
// Two identical buttons that send you to two different OAuth apps is a place
// people get stuck, and the stuck person cannot tell them apart by looking.
//
// It only ever touches the GENERATED label. A row that filled in label_ja /
// label_en wrote what its administrator wants on the button, and appending to that
// would override a deliberate choice — so the caller applies this to the fallback
// only, and the existing precedence (row label > generated) is unchanged.
//
// The display name is preferred over the slug because it is what a human calls that
// company; the slug is the fallback for a tenant that never set one.
func TenantLabelSuffix(base string, tn store.TenantRef, lang string) string {
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

// TenantLinkClaimAllowed reports membership in the closed set above. The set is a
// map and therefore a variable; callers outside this package go through this
// function so that nobody ends up reading a copy of it.
func TenantLinkClaimAllowed(claim string) bool { return TenantLinkClaims[claim] }
