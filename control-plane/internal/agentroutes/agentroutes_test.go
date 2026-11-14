package agentroutes

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
)

// The snapshot's equality with the Agent is held on the Agent side (TestRouteCatalogSnapshot);
// this pins what the CP relies on when it reads it.
func TestSnapshotShape(t *testing.T) {
	var ids []string
	for _, r := range Routes() {
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Errorf("route ids = %v, want non-empty, sorted and unique", ids)
	}
	// Every kind the CP accepts at create has an entry here, so a missing row cannot read as
	// "this harness declares nothing" by accident when it is merely absent.
	for _, k := range agentkinds.All() {
		found := false
		for _, e := range snapshot.Kinds {
			found = found || e.Kind == k.Kind
		}
		if !found {
			t.Errorf("kind %q is in the kind catalog but not in the route catalog", k.Kind)
		}
	}
	for _, e := range snapshot.Kinds {
		for _, id := range append(slices.Clone(e.Routes), e.Fixed...) {
			if !slices.Contains(ids, id) {
				t.Errorf("kind %q declares %q, which is not a route", e.Kind, id)
			}
		}
	}
	for _, o := range append(slices.Clone(snapshot.Chat), snapshot.Imagegen...) {
		if len(o.Routes) == 0 {
			t.Errorf("%q declares no route", o.ID)
		}
		for _, id := range o.Routes {
			if !slices.Contains(ids, id) {
				t.Errorf("%q declares %q, which is not a route", o.ID, id)
			}
		}
	}
	for _, r := range Routes() {
		if r.Class == "" || r.Destination == "" {
			t.Errorf("%s: class %q destination %q, both required (ADR 0138 D1c)", r.ID, r.Class, r.Destination)
		}
	}
}

// A fixed route says what a kind runs on by itself; it never makes an explicit selection valid,
// so declaring them changes nothing a create accepts (ADR 0138 M0: no behaviour change).
func TestFixedRoutesAreNotSelectable(t *testing.T) {
	for _, k := range agentkinds.All() {
		for _, id := range FixedRoutesOf(k.Kind) {
			if slices.Contains(RoutesOf(k.Kind), id) {
				continue
			}
			if code, _ := CheckCreateRoute(k.Kind, &Selection{Route: id}); code != CodeRouteUnsupported {
				t.Errorf("%s on its fixed route %q: code %q, want %s", k.Kind, id, code, CodeRouteUnsupported)
			}
		}
	}
	if got := FixedRoutesOf("codex"); !slices.Equal(got, []string{"chatgpt-subscription", "openai-api"}) {
		t.Errorf("codex fixed routes = %v", got)
	}
	if got := ChatRoutesOf("muse"); !slices.Equal(got, []string{"meta-api", "muse-login"}) {
		t.Errorf("muse chat routes = %v", got)
	}
	if got := ImagegenRoutesOf("codex"); !slices.Equal(got, []string{"chatgpt-subscription", "openai-api"}) {
		t.Errorf("codex image routes = %v", got)
	}
	if r, ok := Lookup("member-llamacpp"); !ok || !r.PerConnection || r.Destination != "unclassified" {
		t.Errorf("member-llamacpp = %+v %v, want per-connection and unclassified", r, ok)
	}
}

func TestCheckCreateRoute(t *testing.T) {
	// Only claude declares routes so far (anthropic-api, anthropic-subscription, bedrock); every other
	// kind refuses any well-formed selection, and claude refuses one it does not declare.
	for _, k := range agentkinds.All() {
		code, err := CheckCreateRoute(k.Kind, &Selection{Route: "anthropic-api"})
		if k.Kind == "claude" {
			if code != "" || err != nil {
				t.Errorf("claude on anthropic-api: %q %v, want it accepted", code, err)
			}
			continue
		}
		if code != CodeRouteUnsupported || err == nil {
			t.Errorf("%s: %q %v, want %s", k.Kind, code, err, CodeRouteUnsupported)
		}
	}
	if code, err := CheckCreateRoute("claude", &Selection{Route: "bedrock", Connection: "p1@us-east-1"}); code != "" || err != nil {
		t.Errorf("claude on bedrock: %q %v, want it accepted", code, err)
	}
	if code, _ := CheckCreateRoute("claude", &Selection{Route: "vertex"}); code != CodeRouteUnsupported {
		t.Errorf("claude on vertex: %q, want %s", code, CodeRouteUnsupported)
	}
	// The provided Bedrock routes (ADR 0129) are catalogued from Q0 and declared by a harness
	// from its phase on: claude declares bedrock-deployment from Q1 (its use is the CP's live
	// check, providedRouteCreateRefusal), and every other kind still refuses both.
	for _, id := range []string{"bedrock-tenant", "bedrock-deployment"} {
		if !slices.ContainsFunc(Routes(), func(r Route) bool { return r.ID == id && r.Wire == "bedrock" }) {
			t.Errorf("%s is not in the snapshot as a bedrock route", id)
		}
		for _, k := range agentkinds.All() {
			if k.Kind == "claude" && id == "bedrock-deployment" {
				if code, err := CheckCreateRoute(k.Kind, &Selection{Route: id}); code != "" || err != nil {
					t.Errorf("claude on %s: %q %v, want it accepted", id, code, err)
				}
				continue
			}
			if code, err := CheckCreateRoute(k.Kind, &Selection{Route: id}); code != CodeRouteUnsupported || err == nil {
				t.Errorf("%s on %s: %q %v, want %s", k.Kind, id, code, err, CodeRouteUnsupported)
			}
		}
	}
	if code, err := CheckCreateRoute("", &Selection{Route: "anthropic-subscription"}); code != "" || err != nil {
		t.Errorf("empty kind (claude) on anthropic-subscription: %q %v", code, err)
	}
	for name, sel := range map[string]*Selection{
		"empty":   {},
		"blank":   {Route: " "},
		"unknown": {Route: "nope"},
	} {
		if code, err := CheckCreateRoute("claude", sel); code != CodeBadRoute || err == nil {
			t.Errorf("%s: %q %v, want %s", name, code, err, CodeBadRoute)
		}
	}
	// The invariant: no selection passes, for every kind and for the empty kind.
	for _, kind := range append([]string{""}, "claude", "codex", "shell", "not-a-kind") {
		if code, err := CheckCreateRoute(kind, nil); code != "" || err != nil {
			t.Errorf("kind %q with no selection: %q %v, want pass", kind, code, err)
		}
	}
}
