package mcpreg

// One grant per tenant client and issuer (ADR 0146 decision 4).
//
// Tenant definitions that carry the same pre-registered client at the same issuer form a group.
// A sign-in to any of them that is made with that client asks for every enabled https definition
// of the group at once (each URL an RFC 8707 resource, the union of their scopes), and the one
// grant it stores lists them as its members. The grant serves a definition only while the
// definition is still in the group at the URL the grant recorded for it: that is the relay's "a
// URL in the grant's set" check, and a definition added to the group, moved to another URL or
// given a scope its member entry lacks reads as "needs more permission" rather than as a second
// sign-in.
//
// Every other grant is the definition's own, as before: one member, itself. So is a grant stored
// before groups existed (no Group): it serves the server it was made for and ends with it.
//
// The URLs come from the tenant's definitions, not from each server's own metadata: the admin who
// bound the client to those URLs is the one who says they belong to it, and the CP bridge refuses
// a URL that is not one of that client's rows.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// maxSharedResources is how many URLs one shared sign-in asks for; the CP bridge refuses more
// (bridgeMaxResources). maxSharedResourceBytes bounds their joined length, so the authorization
// URL stays within what servers commonly accept (8 KiB) and the CP's form limit. A larger group,
// or one whose joined scopes exceed the scope limit, signs each server in on its own, as before
// sharing.
const (
	maxSharedResources     = 32
	maxSharedResourceBytes = 4096
)

var (
	noShareMu sync.Mutex
	// noShare holds the groups whose authorization server refused a sign-in for several
	// resources (invalid_target): their next sign-ins ask for one server each. In memory: a
	// restart tries sharing again, at the cost of one refused sign-in.
	noShare = map[string]bool{}
)

func markNoShare(group string) {
	noShareMu.Lock()
	defer noShareMu.Unlock()
	noShare[group] = true
}

// sharingRefused also reads the store: a grant the AS refused to renew for its set says so after
// a restart too.
func sharingRefused(group string) bool {
	noShareMu.Lock()
	refused := noShare[group]
	noShareMu.Unlock()
	if refused {
		return true
	}
	if data, err := secrets.Load(); err == nil {
		for _, g := range data.MCPOAuth {
			if g.Group == group && g.SetRefused {
				return true
			}
		}
	}
	return false
}

// sharedSetFits reports whether one sign-in can ask for resources and scope together.
func sharedSetFits(resources []string, scope string) bool {
	n := 0
	for _, r := range resources {
		n += len(url.QueryEscape(r)) + len("&resource=")
	}
	return n <= maxSharedResourceBytes && validScope(scope)
}

// groupIssuer is an issuer as a group key compares it: only a root path "/" is dropped (ADR 0146
// decision 2's equivalence). "https://as.example.com/realm" and ".../realm/" stay two issuers.
func groupIssuer(iss string) string {
	if t := strings.TrimSuffix(iss, "/"); t != iss && issuerEquivalent(t, iss) {
		return t
	}
	return iss
}

// grantGroup is the key of the grant d shares with the other definitions of the same tenant
// client, "" when d's grant is its own. Confidential is part of it: a grant made with a public
// client renews at the AS, one made with a confidential client through the CP, so a switch
// between the two is another client. A plain-http server is in no group: a shared bearer is
// never sent in the clear, so it signs in on its own and its disconnect touches nobody else.
func grantGroup(d ServerDef) string {
	c := d.OAuthClient
	if d.Origin != OriginTenant || d.Auth != AuthOAuth || c == nil || !validTenantClient(*c) || !sharedURL(d.URL) {
		return ""
	}
	auth := "none"
	if c.Confidential {
		auth = tokenAuthCP
	}
	return groupIssuer(c.Issuer) + "\n" + c.ClientID + "\n" + auth
}

// grantMember is the entry g holds for serverID. A grant made before members existed covers its
// own server at its own resource, with the scopes unknown.
func grantMember(g *secrets.MCPOAuthGrant, serverID string) (secrets.MCPOAuthGrantMember, bool) {
	if len(g.Members) == 0 {
		if g.ServerID == serverID {
			return secrets.MCPOAuthGrantMember{ServerID: serverID, Resource: g.Resource}, true
		}
		return secrets.MCPOAuthGrantMember{}, false
	}
	for _, m := range g.Members {
		if m.ServerID == serverID {
			return m, true
		}
	}
	return secrets.MCPOAuthGrantMember{}, false
}

// grantApplies reports whether g is a grant of d's: a group's grant applies to every definition of
// the group, member or not (a definition the group gained reads as needing more permission, not as
// signed out), except one that disconnected from it; any other grant applies to the definition it
// has a member entry for. A tenant client's
// grant made before groups existed still needs d to carry that client, as it did then.
func grantApplies(g *secrets.MCPOAuthGrant, d ServerDef) bool {
	if g.Group != "" {
		refusedMu.Lock()
		left := refused[grantKey(g)][d.ID]
		refusedMu.Unlock()
		if left {
			return false
		}
		for _, id := range g.Left {
			if id == d.ID {
				return false
			}
		}
		return g.Group == grantGroup(d)
	}
	if _, member := grantMember(g, d.ID); !member {
		return false
	}
	if g.Registration != OAuthRegTenant {
		return true
	}
	c := d.OAuthClient
	return d.Origin == OriginTenant && c != nil && sameIssuer(c.Issuer, g.Issuer) && c.ClientID == g.ClientID &&
		c.Confidential == (g.TokenAuth == tokenAuthCP)
}

// grantResource is the resource g was made for at d, when g may be sent to d now: g applies to d,
// d has a member entry, and d's URL is the one the entry recorded.
func grantResource(g *secrets.MCPOAuthGrant, d ServerDef) (string, bool) {
	if !grantApplies(g, d) {
		return "", false
	}
	m, ok := grantMember(g, d.ID)
	if !ok {
		return "", false
	}
	u, err := url.Parse(d.URL)
	if err != nil || !relayBound(m.Resource, u) {
		return "", false
	}
	return m.Resource, true
}

// tokenCovers reports whether g's current access token was requested for res. A grant that does
// not record it (made before it existed) was requested for its own resource.
func tokenCovers(g *secrets.MCPOAuthGrant, res string) bool {
	if len(g.TokenResources) == 0 {
		return res == g.Resource
	}
	for _, r := range g.TokenResources {
		if r == res {
			return true
		}
	}
	return false
}

// missingScopes lists the scopes d carries that its member entry was not asked for: all of them
// when it has none. A grant made before members existed is taken to have its own server's: it
// reads as it did then.
func missingScopes(g *secrets.MCPOAuthGrant, d ServerDef) []string {
	m, ok := grantMember(g, d.ID)
	if ok && len(g.Members) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, s := range m.Scopes {
		have[s] = true
	}
	var out []string
	for _, s := range d.Scopes {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

// findGrantFor returns a copy of the grant d uses among those that apply to it. One that serves d
// wins over one that does not, then the newest: a definition signed in on its own (a DCR retry)
// next to its group's grant uses whichever sign-in is newer (stored later, on a tie).
func findGrantFor(data *secrets.Data, d ServerDef) *secrets.MCPOAuthGrant {
	var best *secrets.MCPOAuthGrant
	bestServes := false
	for i := range data.MCPOAuth {
		g := data.MCPOAuth[i]
		if !grantApplies(&g, d) {
			continue
		}
		_, serves := grantResource(&g, d)
		if best == nil || (serves && !bestServes) || (serves == bestServes && g.ConnectedAt >= best.ConnectedAt) {
			best, bestServes = &g, serves
		}
	}
	return best
}

// grantLoad reads the store and the effective registry once, with serverID's definition. A
// server the registry no longer names is looked up by its id alone (a grant of its own).
func grantLoad(serverID string) (*secrets.Data, Registry, ServerDef, error) {
	data, err := secrets.Load()
	if err != nil {
		return nil, Registry{}, ServerDef{}, err
	}
	reg := compose(data, loadTenantCache(), loadOptOut())
	return data, reg, defIn(reg, serverID), nil
}

func defIn(reg Registry, id string) ServerDef {
	for _, d := range reg.Servers {
		if d.ID == id {
			return d
		}
	}
	return ServerDef{ID: id}
}

// sharedURL: a shared bearer goes to https only (http on loopback, for development). A peer is
// not signed in on its own, so nothing else would stop a plain-http row from receiving it.
func sharedURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())))
}

// groupPeers are the other definitions a sign-in of d with its tenant client also asks for:
// enabled https OAuth definitions of d's group, in registry order. ok is false when the group is
// too large for one sign-in, which then covers d alone.
func groupPeers(reg Registry, d ServerDef) (peers []ServerDef, ok bool) {
	group := grantGroup(d)
	if group == "" {
		return nil, true
	}
	for _, o := range reg.Servers {
		if o.ID != d.ID && o.Enabled && o.Transport == TransportHTTP && grantGroup(o) == group {
			if _, canon := canonicalResource(o.URL); canon {
				peers = append(peers, o)
			}
		}
	}
	if len(peers)+1 > maxSharedResources {
		return nil, false
	}
	return peers, true
}

// memberStillValid reports whether m is still what a sign-in for group asked for: its definition
// is in the group, at m's URL.
func memberStillValid(reg Registry, m secrets.MCPOAuthGrantMember, group string) bool {
	d := defIn(reg, m.ServerID)
	u, err := url.Parse(d.URL)
	return err == nil && grantGroup(d) == group && relayBound(m.Resource, u)
}

// memberResources are the members' resources, deduplicated as whole strings (a URL may hold a
// comma, so it is never split).
func memberResources(members []secrets.MCPOAuthGrantMember) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range members {
		if !seen[m.Resource] {
			seen[m.Resource] = true
			out = append(out, m.Resource)
		}
	}
	return out
}

// servedResources are the resources of g's members that g still serves: what a renewal asks for.
// A member whose definition left, moved or vanished is not asked for again, so the CP bridge,
// which refuses a URL that is no longer one of the client's rows, is not sent one.
func servedResources(g *secrets.MCPOAuthGrant, reg Registry) []string {
	if len(g.Members) == 0 {
		return []string{g.Resource}
	}
	var served []secrets.MCPOAuthGrantMember
	for _, m := range g.Members {
		if _, ok := grantResource(g, defIn(reg, m.ServerID)); ok {
			served = append(served, m)
		}
	}
	return memberResources(served)
}

// sameSignIn reports whether a and b are the same stored sign-in: the server it was started from
// and its generation (a renewal keeps both).
func sameSignIn(a, b *secrets.MCPOAuthGrant) bool {
	return a.ServerID == b.ServerID && grantGeneration(a) == grantGeneration(b)
}

// grantKey is the lock of one stored sign-in: the server it was started from and its generation,
// which a renewal keeps. Renewals and a disconnect of that sign-in hold it, so it stays the same
// across a rotation and no request of one sign-in overtakes another; members of a shared grant use
// one sign-in and so one lock, and grants of other sign-ins never wait for each other.
func grantKey(g *secrets.MCPOAuthGrant) string {
	return "grant\n" + g.ServerID + "\n" + grantGeneration(g)
}

// credKey names a refresh token of one client, hashed: what spent records.
func credKey(g *secrets.MCPOAuthGrant, refresh string) string {
	sum := sha256.Sum256([]byte(refresh))
	return g.Issuer + "\n" + g.ClientID + "\n" + hex.EncodeToString(sum[:16])
}

// signInLockKey orders a sign-in's completion against a disconnect of what it would store: per
// group for a shared grant, per server otherwise.
func signInLockKey(serverID, group string) string {
	if group != "" {
		return "group\n" + group
	}
	return serverLockKey(serverID)
}

// sharedNames are the names of the other definitions g, the grant d uses, is shared with: the
// ones a disconnect here ends too. None for a grant of d's own.
func sharedNames(reg Registry, d ServerDef, g *secrets.MCPOAuthGrant) []string {
	if g == nil || g.Group == "" {
		return nil
	}
	var out []string
	for _, o := range reg.Servers {
		if o.ID != d.ID && grantApplies(g, o) {
			out = append(out, o.Name)
		}
	}
	return out
}
