package routepolicysrv

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
)

func hiddenIDs(items []hiddenItemWire) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.ID)
	}
	return out
}

func hiddenItem(items []hiddenItemWire, id string) (hiddenItemWire, bool) {
	for _, i := range items {
		if i.ID == id {
			return i, true
		}
	}
	return hiddenItemWire{}, false
}

// The tenant view lists what the effective policy hides from the members, with the layers that
// hide it (D10a): nothing with no restriction; under a cap with no subscriptions, the
// subscription-only harnesses, claude's chat and agy's image provider, each by the cap; and a
// harness hidden by two layers names both, in the layers' order. A harness with any allowed route
// is not hidden.
func TestTenantViewListsWhatIsHidden(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "")
	base := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	v := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	if len(v.Hidden.Kinds)+len(v.Hidden.Chat)+len(v.Hidden.Imagegen) != 0 || v.Hidden.Kinds == nil {
		t.Fatalf("unrestricted: %+v", v.Hidden)
	}

	noSubs := `{"classes":["member-key","member-cloud","provided","self-hosted"]}`
	if w := routeAdminDo(mux, "PUT", base+"/cap", "root-x", noSubs); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	v = decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	for _, k := range []string{"agy", "copilot", "cursor", "kiro"} {
		it, ok := hiddenItem(v.Hidden.Kinds, k)
		if !ok || !slices.Equal(it.ForbiddenBy, []string{routepolicy.LayerTenantCap}) {
			t.Errorf("%s: hidden %v, item %+v", k, ok, it)
		}
	}
	for _, k := range []string{"claude", "codex", "opencode", "lcpp", "muse"} {
		if _, ok := hiddenItem(v.Hidden.Kinds, k); ok {
			t.Errorf("%s is listed hidden although some of its routes are allowed", k)
		}
	}
	if !slices.Equal(hiddenIDs(v.Hidden.Chat), []string{"agy", "claude", "cursor"}) {
		t.Errorf("hidden chat backends %v", hiddenIDs(v.Hidden.Chat))
	}
	if !slices.Equal(hiddenIDs(v.Hidden.Imagegen), []string{"agy"}) {
		t.Errorf("hidden image providers %v", hiddenIDs(v.Hidden.Imagegen))
	}

	// The tenant takes claude's remaining routes away too: claude is hidden by both layers.
	if w := routeAdminDo(mux, "PUT", base, "boss-x", `{"deny":["anthropic-api","bedrock","bedrock-deployment"]}`); w.Code != 200 {
		t.Fatalf("tenant rule: %d %s", w.Code, w.Body)
	}
	v = decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	it, ok := hiddenItem(v.Hidden.Kinds, "claude")
	if !ok || !slices.Equal(it.ForbiddenBy, []string{routepolicy.LayerTenantCap, routepolicy.LayerTenant}) ||
		!slices.Equal(it.Routes, []string{"anthropic-api", "anthropic-subscription", "bedrock", "bedrock-deployment"}) {
		t.Errorf("claude: hidden %v, item %+v", ok, it)
	}
	if !v.HiddenKinds()["claude"] || v.HiddenKinds()["codex"] {
		t.Errorf("HiddenKinds %v", v.HiddenKinds())
	}
	// AllowedRoutes reads the same decision: claude has none left, opencode keeps its own.
	if got := v.AllowedRoutes(); len(got["claude"]) != 0 || len(got["opencode"]) == 0 {
		t.Errorf("AllowedRoutes %v", got)
	}
}

// AllowedRoutes lists the routes the layers allow per harness: the denied one drops out.
func TestAllowedRoutesDropsDeniedRoute(t *testing.T) {
	var tenant routepolicy.Rule
	if err := json.Unmarshal([]byte(`{"deny":["bedrock"]}`), &tenant); err != nil {
		t.Fatal(err)
	}
	owners, _ := foldOwners(routepolicy.Layers{Tenant: tenant}, nil, 0)
	got := tenantPolicyWire{Owners: owners}.AllowedRoutes()
	if slices.Contains(got["claude"], "bedrock") || !slices.Contains(got["claude"], "anthropic-api") {
		t.Errorf("claude allowed %v", got["claude"])
	}
	if slices.Contains(got["opencode"], "bedrock") {
		t.Errorf("opencode allowed %v", got["opencode"])
	}
}

// A per-connection route allowed on an approved endpoint keeps its harness visible to the tenant:
// lcpp is hidden while it has no approved host, and not once one is approved.
func TestHiddenCountsApprovedEndpoints(t *testing.T) {
	var tenant routepolicy.Rule
	if err := json.Unmarshal([]byte(`{"deny":["af-engine-llm"]}`), &tenant); err != nil {
		t.Fatal(err)
	}
	layers := routepolicy.Layers{Tenant: tenant}
	_, h0 := foldOwners(layers, []string{"member-llamacpp"}, 0)
	_, h1 := foldOwners(layers, []string{"member-llamacpp"}, 1)
	if _, ok := hiddenItem(h0.Kinds, "lcpp"); !ok {
		t.Error("lcpp with no approved host is not listed hidden")
	}
	if _, ok := hiddenItem(h1.Kinds, "lcpp"); ok {
		t.Error("lcpp with an approved host is listed hidden")
	}
}

func ownerItem(items []ownerItemWire, id string) (ownerItemWire, bool) {
	for _, i := range items {
		if i.ID == id {
			return i, true
		}
	}
	return ownerItemWire{}, false
}

// owners and hidden are folded from one decision: over several layer combinations (none, one
// layer, a harness hidden by two layers, an approved endpoint), an item is in hidden if and only
// if its allowed list is empty, and allowed is always a subset of routes.
func TestOwnersAgreeWithHidden(t *testing.T) {
	rule := func(s string) routepolicy.Rule {
		var r routepolicy.Rule
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	noSubs := `{"classes":["member-key","member-cloud","provided","self-hosted"]}`
	cases := []struct {
		name     string
		layers   routepolicy.Layers
		epRoutes []string
		approved int
	}{
		{name: "unrestricted"},
		{name: "cap only", layers: routepolicy.Layers{TenantCap: rule(noSubs)}},
		{name: "tenant only", layers: routepolicy.Layers{Tenant: rule(`{"deny":["anthropic-api","bedrock"]}`)}},
		{name: "two layers", layers: routepolicy.Layers{TenantCap: rule(noSubs),
			Tenant: rule(`{"deny":["anthropic-api","bedrock","bedrock-deployment"]}`)}},
		{name: "deployment and tenant", layers: routepolicy.Layers{Deployment: rule(noSubs), Tenant: rule(`{"deny":["af-engine-llm"]}`)}},
		{name: "endpoint approved", layers: routepolicy.Layers{Tenant: rule(`{"deny":["af-engine-llm"]}`)},
			epRoutes: []string{"member-llamacpp"}, approved: 1},
		{name: "endpoint not approved", layers: routepolicy.Layers{Tenant: rule(`{"deny":["af-engine-llm"]}`)},
			epRoutes: []string{"member-llamacpp"}},
	}
	sawHidden, sawVisible := false, false
	for _, c := range cases {
		o, h := foldOwners(c.layers, c.epRoutes, c.approved)
		for _, g := range []struct {
			name string
			os   []ownerItemWire
			hs   []hiddenItemWire
		}{{"kinds", o.Kinds, h.Kinds}, {"chat", o.Chat, h.Chat}, {"imagegen", o.Imagegen, h.Imagegen}} {
			for _, it := range g.os {
				_, isHidden := hiddenItem(g.hs, it.ID)
				if isHidden != (len(it.Allowed) == 0) {
					t.Errorf("%s/%s/%s: hidden=%v allowed=%v", c.name, g.name, it.ID, isHidden, it.Allowed)
				}
				for _, r := range it.Allowed {
					if !slices.Contains(it.Routes, r) {
						t.Errorf("%s/%s/%s: allowed %q is not one of %v", c.name, g.name, it.ID, r, it.Routes)
					}
				}
				sawHidden = sawHidden || isHidden
				sawVisible = sawVisible || !isHidden
			}
			for _, hi := range g.hs {
				if _, ok := ownerItem(g.os, hi.ID); !ok {
					t.Errorf("%s/%s: hidden %s is missing from owners", c.name, g.name, hi.ID)
				}
			}
		}
	}
	if !sawHidden || !sawVisible {
		t.Fatalf("the cases must cover both hidden and visible items (hidden %v, visible %v)", sawHidden, sawVisible)
	}
}

// The view carries owners: unrestricted, every route of every owner is allowed; under a cap with no
// subscriptions claude keeps its API routes and loses the subscription one; a per-connection route
// is allowed once an approved endpoint exists.
func TestTenantViewCarriesOwners(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "")
	base := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	v := decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	if v.Owners.Kinds == nil || v.Owners.Chat == nil || v.Owners.Imagegen == nil {
		t.Fatalf("owners %+v", v.Owners)
	}
	for name, items := range map[string][]ownerItemWire{"kinds": v.Owners.Kinds, "chat": v.Owners.Chat, "imagegen": v.Owners.Imagegen} {
		if len(items) == 0 {
			t.Errorf("unrestricted %s: no owners", name)
		}
		for _, k := range items {
			if !slices.Equal(k.Routes, k.Allowed) {
				t.Errorf("unrestricted %s/%s: routes %v allowed %v", name, k.ID, k.Routes, k.Allowed)
			}
		}
	}
	noSubs := `{"classes":["member-key","member-cloud","provided","self-hosted"]}`
	if w := routeAdminDo(mux, "PUT", base+"/cap", "root-x", noSubs); w.Code != 200 {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
	v = decodePolicy(t, routeAdminDo(mux, "GET", base, "boss-x", "").Body.String())
	c, ok := ownerItem(v.Owners.Kinds, "claude")
	if !ok || !slices.Equal(c.Routes, []string{"anthropic-api", "anthropic-subscription", "bedrock", "bedrock-deployment"}) ||
		slices.Contains(c.Allowed, "anthropic-subscription") || !slices.Contains(c.Allowed, "anthropic-api") {
		t.Errorf("claude under the cap: %+v", c)
	}
	if a, ok := ownerItem(v.Owners.Kinds, "agy"); !ok || len(a.Allowed) != 0 || a.Allowed == nil {
		t.Errorf("agy under the cap: %+v (allowed must be an empty list, not null)", a)
	}
}
