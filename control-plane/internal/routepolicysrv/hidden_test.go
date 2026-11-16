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
}

// A per-connection route allowed on an approved endpoint keeps its harness visible to the tenant:
// lcpp is hidden while it has no approved host, and not once one is approved.
func TestHiddenCountsApprovedEndpoints(t *testing.T) {
	var tenant routepolicy.Rule
	if err := json.Unmarshal([]byte(`{"deny":["af-engine-llm"]}`), &tenant); err != nil {
		t.Fatal(err)
	}
	layers := routepolicy.Layers{Tenant: tenant}
	if _, ok := hiddenItem(hiddenOf(layers, []string{"member-llamacpp"}, 0).Kinds, "lcpp"); !ok {
		t.Error("lcpp with no approved host is not listed hidden")
	}
	if _, ok := hiddenItem(hiddenOf(layers, []string{"member-llamacpp"}, 1).Kinds, "lcpp"); ok {
		t.Error("lcpp with an approved host is listed hidden")
	}
}
