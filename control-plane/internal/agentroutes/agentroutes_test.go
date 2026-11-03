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
		for _, id := range e.Routes {
			if !slices.Contains(ids, id) {
				t.Errorf("kind %q declares %q, which is not a route", e.Kind, id)
			}
		}
	}
}

func TestCheckCreateRoute(t *testing.T) {
	// Only claude declares routes so far (anthropic-api, anthropic-subscription); every other
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
	if code, _ := CheckCreateRoute("claude", &Selection{Route: "bedrock"}); code != CodeRouteUnsupported {
		t.Errorf("claude on bedrock: %q, want %s", code, CodeRouteUnsupported)
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
