package main

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Per-tenant login rules (docs/log/61 §61.9 + ADR0043 決定 15/16/19), and the short-TTL
// cache the entry gate reads them through.
//
// The design turns on keeping THREE layers apart (§61.9.2), and this file only
// serves the first two:
//
//	entry gate    — may this person sign in to this deployment at all?
//	                authGate, every request. Knows no tenant.
//	tenant gate   — may this person use THIS tenant right now?
//	                resolveFull / selectMembership.
//	login page    — which buttons to show. Cosmetic; never an authorization answer.
//
// ★ Why a cache at all: the entry gate runs on every request and now consults the
// database. That sounds heavier than it is — the allowlist FILE this replaces is
// re-read with os.ReadFile on every request already (§61.9.7). A 30s TTL plus an
// explicit drop on every admin write keeps "remove someone and it takes effect
// immediately" true, which is the property offboarding depends on.

// tenantRuleTTL is short on purpose: it bounds how long a rule change takes to
// reach a deployment whose admin API is served by another replica (the drop below
// only reaches this process).
const tenantRuleTTL = 30 * time.Second

// tenantLoginStore is the narrow store view this cache needs.
type tenantLoginStore interface {
	ListTenantLoginRules(ctx context.Context) ([]store.TenantLoginRules, error)
	EmailHasActiveMembership(ctx context.Context, email string) (bool, error)
}

// tenantRuleSnapshot is one consistent read of every tenant's rules.
type tenantRuleSnapshot struct {
	byID   map[string]store.TenantLoginRules
	bySlug map[string]store.TenantLoginRules
	// autoJoin maps an auto-join domain to the tenant that wins it. Two tenants
	// claiming one domain is a configuration error the admin API refuses to create;
	// if one exists anyway (edited before this version, or two admins racing), the
	// LOWEST SLUG wins so the outcome is at least deterministic rather than
	// whichever row the database happened to return first (§61.9.8).
	autoJoin map[string]store.TenantLoginRules
	// contested lists the domains more than one tenant claims, so the auto-join
	// path can say so in the audit ledger rather than silently picking one.
	contested map[string]bool
}

type tenantLoginCache struct {
	store tenantLoginStore
	ttl   time.Duration

	mu       sync.Mutex
	snap     *tenantRuleSnapshot
	snapExp  time.Time
	members  map[string]memberAnswer
	warnedAt string // last contested-domain set we logged, to not repeat every refresh
}

type memberAnswer struct {
	ok  bool
	exp time.Time
}

func newTenantLoginCache(st tenantLoginStore) *tenantLoginCache {
	return &tenantLoginCache{store: st, ttl: tenantRuleTTL, members: map[string]memberAnswer{}}
}

// invalidate drops everything. Called by every admin write that can change who may
// enter (tenant rules, membership add/remove/role) so the effect is immediate.
func (c *tenantLoginCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap, c.snapExp, c.members = nil, time.Time{}, map[string]memberAnswer{}
	c.mu.Unlock()
}

// snapshot returns the current rules, refreshing when stale.
//
// A refresh failure returns the STALE snapshot when there is one: the database
// being briefly unavailable must not turn into "nobody is a member of anything",
// which would both deny logins and, worse, make the auto-join path create
// duplicate memberships. With no snapshot at all it returns nil and every caller
// treats that as "no tenant rules", which is fail-closed for the tenant gate
// (nothing is allowed to pass a rule that could not be read) and neutral for the
// entry gate (the deployment allowlist still applies).
func (c *tenantLoginCache) snapshot(ctx context.Context) *tenantRuleSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.snap != nil && time.Now().Before(c.snapExp) {
		s := c.snap
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	rules, err := c.store.ListTenantLoginRules(ctx)
	if err != nil {
		log.Printf("tenant login rules: %v (using the previous snapshot)", err)
		c.mu.Lock()
		s := c.snap
		c.mu.Unlock()
		return s
	}
	snap := buildTenantRuleSnapshot(rules)

	c.mu.Lock()
	c.snap, c.snapExp = snap, time.Now().Add(c.ttl)
	if key := contestedKey(snap.contested); key != c.warnedAt {
		c.warnedAt = key
		if key != "" {
			log.Printf("WARNING: auto_join_domains is claimed by more than one tenant (%s) — the lowest slug wins; fix the duplicate in the admin UI", key)
		}
	}
	c.mu.Unlock()
	return snap
}

func buildTenantRuleSnapshot(rules []store.TenantLoginRules) *tenantRuleSnapshot {
	s := &tenantRuleSnapshot{
		byID:      make(map[string]store.TenantLoginRules, len(rules)),
		bySlug:    make(map[string]store.TenantLoginRules, len(rules)),
		autoJoin:  map[string]store.TenantLoginRules{},
		contested: map[string]bool{},
	}
	sorted := append([]store.TenantLoginRules(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, r := range sorted {
		s.byID[r.ID] = r
		s.bySlug[r.Slug] = r
		for _, d := range r.AutoJoinDomains {
			if prev, dup := s.autoJoin[d]; dup {
				if prev.Slug != r.Slug {
					s.contested[d] = true
				}
				continue // first (lowest slug) wins
			}
			s.autoJoin[d] = r
		}
	}
	return s
}

func contestedKey(m map[string]bool) string {
	if len(m) == 0 {
		return ""
	}
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// --- the entry gate's DB-derived term (docs/log/61 §61.9.6) ---------------------

// entryAllowed reports whether the DATABASE says this address may reach the login:
// an auto-join domain matches, or the person is on some tenant's roster.
//
// ★ This is only one term of the entry gate. The provider's own list (or the
// deployment-wide one) is the other, and the two are OR'd — see oidcProvider.Allowed.
// Being let in here means nothing about which tenant, if any, the person can use;
// that is the tenant gate's question, and someone who passes here with no membership
// still lands on the same not_provisioned page as today.
func (c *tenantLoginCache) entryAllowed(ctx context.Context, email string) bool {
	if c == nil {
		return false
	}
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	if snap := c.snapshot(ctx); snap != nil {
		if _, ok := snap.autoJoin[email[at+1:]]; ok {
			return true
		}
	}
	return c.hasMembership(ctx, email)
}

// hasMembership answers "is this address on any roster", cached per address for the
// same short TTL. Caching per address rather than loading the whole roster keeps
// the memory cost proportional to who is actually signing in.
func (c *tenantLoginCache) hasMembership(ctx context.Context, email string) bool {
	c.mu.Lock()
	if a, ok := c.members[email]; ok && time.Now().Before(a.exp) {
		c.mu.Unlock()
		return a.ok
	}
	c.mu.Unlock()

	ok, err := c.store.EmailHasActiveMembership(ctx, email)
	if err != nil {
		// Do not cache an error as a denial: the next request re-asks.
		log.Printf("tenant login: membership lookup for %s: %v", email, err)
		return false
	}
	c.mu.Lock()
	if len(c.members) > 4096 { // bound an unbounded key space (login attempts)
		c.members = map[string]memberAnswer{}
	}
	c.members[email] = memberAnswer{ok: ok, exp: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return ok
}

// --- the tenant gate (docs/log/61 §61.9.4 / §61.9.8) ----------------------------

// autoJoinTenant returns the tenant an address joins automatically, if any.
// contested reports that more than one tenant claimed the domain, so the caller can
// record the fact instead of quietly picking one.
func (c *tenantLoginCache) autoJoinTenant(ctx context.Context, email string) (t store.TenantLoginRules, contested, ok bool) {
	if c == nil {
		return store.TenantLoginRules{}, false, false
	}
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return store.TenantLoginRules{}, false, false
	}
	snap := c.snapshot(ctx)
	if snap == nil {
		return store.TenantLoginRules{}, false, false
	}
	domain := email[at+1:]
	r, ok := snap.autoJoin[domain]
	return r, snap.contested[domain], ok
}

// providerAllowed enforces tenant.allowed_providers (決定 14). An empty list means
// "every provider the deployment enabled", which is what an existing single-IdP
// deployment has and why nothing changes for it.
//
// ★ This — not the filtered login page — is the enforcement. Without it, signing in
// on the generic /login with any enabled provider and then swapping X-AF-Tenant
// walks into a tenant that was configured to accept only one IdP.
func (c *tenantLoginCache) providerAllowed(ctx context.Context, tenantID, prov string) (bool, []string) {
	if c == nil {
		return true, nil
	}
	snap := c.snapshot(ctx)
	if snap == nil {
		return true, nil
	}
	r, ok := snap.byID[tenantID]
	if !ok || len(r.AllowedProviders) == 0 {
		return true, nil
	}
	// An unknown provider (AUTH=proxy / AUTH=dev have no IdP to name) cannot be
	// matched against the list. Those modes have no per-tenant IdP story at all, so
	// requiring one would lock them out of every restricted tenant.
	if prov == "" {
		return true, r.AllowedProviders
	}
	for _, p := range r.AllowedProviders {
		if p == prov {
			return true, r.AllowedProviders
		}
	}
	return false, r.AllowedProviders
}

// networkAllowed enforces tenant.allowed_cidrs (docs/log/66, ADR 0047 決定 3). An empty
// list means "any network", which is how a tenant that never set one is unaffected —
// and how the feature is switched off, since there is no operator flag.
//
// ⚠️ An unknown caller address is a DENIAL, not a pass. The address is unknown only
// when the deployment says there are N proxies in front and the forwarding chain does
// not have N entries — i.e. the CP cannot tell who is calling. A restriction nobody
// can evaluate must not be one that everybody passes.
func (c *tenantLoginCache) networkAllowed(ctx context.Context, tenantID string, ip clientIPInfo) bool {
	if c == nil {
		return true
	}
	snap := c.snapshot(ctx)
	if snap == nil {
		return true
	}
	r, found := snap.byID[tenantID]
	if !found || len(r.AllowedCIDRs) == 0 {
		return true
	}
	prefixes, _, err := parseCIDRList(strings.Join(r.AllowedCIDRs, ","))
	if err != nil || len(prefixes) == 0 {
		// Stored text that no longer parses would otherwise lock the tenant out
		// forever with no way back through the UI. Treat it as no rule and let the
		// screen show what is stored so a human can fix it.
		return true
	}
	if !ip.OK {
		return false
	}
	return ipInAny(ip.IP, prefixes)
}

// rulesForSlug returns a tenant's rules by slug (login page rendering).
func (c *tenantLoginCache) rulesForSlug(ctx context.Context, slug string) (store.TenantLoginRules, bool) {
	if c == nil || slug == "" {
		return store.TenantLoginRules{}, false
	}
	snap := c.snapshot(ctx)
	if snap == nil {
		return store.TenantLoginRules{}, false
	}
	r, ok := snap.bySlug[slug]
	return r, ok
}

// --- CSV normalization ------------------------------------------------------

// splitCSVLower parses a stored CSV into lowercased, deduped entries.
func splitCSVLower(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// splitDomainCSV is splitCSVLower for email domains, tolerating a leading "@" the
// way AF_OAUTH_ALLOWED_DOMAINS does.
func splitDomainCSV(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), "@")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// joinCSV renders the normalized form back for storage.
func joinCSV(v []string) string { return strings.Join(v, ",") }

// domainMatches reports whether an email's domain is in the list. An empty list
// means "no constraint" — that is what makes allowed_domains opt-in.
func domainMatches(domains []string, email string) bool {
	if len(domains) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	d := email[at+1:]
	for _, x := range domains {
		if x == d {
			return true
		}
	}
	return false
}
