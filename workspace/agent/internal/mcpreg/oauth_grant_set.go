package mcpreg

// One grant per tenant client and issuer (ADR 0146 decision 4).
//
// Tenant definitions that carry the same pre-registered client at the same issuer form a group.
// A sign-in to any of them that is made with that client asks for every enabled definition of the
// group at once (each URL an RFC 8707 resource, the union of their scopes), and the one grant it
// stores lists them as its members. The grant serves a definition only while the definition is
// still in the group at the URL the grant recorded for it: that is the relay's "a URL in the
// grant's set" check, and a definition added to the group, moved to another URL or given a scope
// its member entry lacks reads as "needs more permission" rather than as a second sign-in.
//
// Every other definition keeps a grant of its own, as before: one member, itself.
//
// The URLs come from the tenant's definitions, not from each server's own metadata: the admin who
// bound the client to those URLs is the one who says they belong to it, and the CP bridge refuses
// a URL that is not one of that client's rows.

import (
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// grantGroup is the key of the grant d shares with the other definitions of the same tenant
// client, "" when d's grant is its own. Confidential is part of it: a grant made with a public
// client renews at the AS, one made with a confidential client through the CP, so a switch
// between the two is another client.
func grantGroup(d ServerDef) string {
	c := d.OAuthClient
	if d.Origin != OriginTenant || d.Auth != AuthOAuth || c == nil || !validTenantClient(*c) {
		return ""
	}
	auth := "none"
	if c.Confidential {
		auth = tokenAuthCP
	}
	return strings.TrimSuffix(c.Issuer, "/") + "\n" + c.ClientID + "\n" + auth
}

// grantGroupOf is the group of a stored grant: set only for one made with a tenant client.
func grantGroupOf(g *secrets.MCPOAuthGrant) string {
	if g.Registration != OAuthRegTenant {
		return ""
	}
	return strings.TrimSuffix(g.Issuer, "/") + "\n" + g.ClientID + "\n" + g.TokenAuth
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

// grantResource is the resource g was made for at d, when g may be sent to d now: d has a member
// entry, is still in the grant's group (a definition that left it is no longer what the grant was
// made for), and its URL is the one the entry recorded.
func grantResource(g *secrets.MCPOAuthGrant, d ServerDef) (string, bool) {
	m, ok := grantMember(g, d.ID)
	if !ok {
		return "", false
	}
	if grp := grantGroupOf(g); grp != "" && grp != grantGroup(d) {
		return "", false
	}
	u, err := url.Parse(d.URL)
	if err != nil || !relayBound(m.Resource, u) {
		return "", false
	}
	return m.Resource, true
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

// grantApplies reports whether g is a grant of d's: a tenant client's grant applies to every
// definition of its group, member or not (a definition the group gained reads as needing more
// permission, not as signed out), and any other grant to the definitions it has a member entry
// for. A tenant grant does not apply to a definition that left its group.
func grantApplies(g *secrets.MCPOAuthGrant, d ServerDef) bool {
	if grp := grantGroupOf(g); grp != "" {
		return grp == grantGroup(d)
	}
	_, member := grantMember(g, d.ID)
	return member
}

// findGrantFor returns a copy of the grant d uses among those that apply to it. One that serves d
// wins over one that does not, then the newest, so two grants left from before sharing each keep
// serving their own server.
func findGrantFor(data *secrets.Data, d ServerDef) *secrets.MCPOAuthGrant {
	var best *secrets.MCPOAuthGrant
	bestServes := false
	for i := range data.MCPOAuth {
		g := data.MCPOAuth[i]
		if !grantApplies(&g, d) {
			continue
		}
		_, serves := grantResource(&g, d)
		if best == nil || (serves && !bestServes) || (serves == bestServes && g.ConnectedAt > best.ConnectedAt) {
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

// groupPeers are the other enabled OAuth definitions in d's group, in registry order.
func groupPeers(reg Registry, d ServerDef) []ServerDef {
	group := grantGroup(d)
	if group == "" {
		return nil
	}
	var out []ServerDef
	for _, o := range reg.Servers {
		if o.ID != d.ID && o.Enabled && o.Transport == TransportHTTP && grantGroup(o) == group {
			out = append(out, o)
		}
	}
	return out
}

// servedResources are the resources of g's members that g still serves, deduplicated: what a
// renewal asks for. A member whose definition left, moved or vanished is not asked for again, so
// the CP bridge, which refuses a URL that is no longer one of the client's rows, is not sent one.
func servedResources(g *secrets.MCPOAuthGrant, reg Registry) []string {
	if len(g.Members) == 0 {
		return []string{g.Resource}
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range g.Members {
		res, ok := grantResource(g, defIn(reg, m.ServerID))
		if ok && !seen[res] {
			seen[res] = true
			out = append(out, res)
		}
	}
	return out
}

// sameSignIn reports whether a and b are the same stored sign-in: the server it was started from
// and its generation (a renewal keeps both).
func sameSignIn(a, b *secrets.MCPOAuthGrant) bool {
	return a.ServerID == b.ServerID && grantGeneration(a) == grantGeneration(b)
}

// grantKey is the lock and pending-rotation key of one stored sign-in.
func grantKey(g *secrets.MCPOAuthGrant) string {
	return g.ServerID + "\n" + grantGeneration(g)
}

// signInLockKey orders a sign-in's completion against a disconnect of what it would store: per
// group for a shared grant, per server otherwise.
func signInLockKey(serverID, group string) string {
	if group != "" {
		return "group\n" + group
	}
	return serverLockKey(serverID)
}

// groupNames are the names of d's group's other definitions, for the Console.
func groupNames(reg Registry, d ServerDef) []string {
	group := grantGroup(d)
	if group == "" {
		return nil
	}
	var out []string
	for _, o := range reg.Servers {
		if o.ID != d.ID && grantGroup(o) == group {
			out = append(out, o.Name)
		}
	}
	return out
}
