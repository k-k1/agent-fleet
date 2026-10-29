package main

// Equivalence proof for the managed lifecycle and connection status capabilities: the loops
// over the registry in main.go, shutdown.go and connections.go must call what the hand wiring
// called, in the order it called it.

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// handWiredLifecycle is the boot reconcile (main.go) and the abort and shutdown calls
// (shutdown.go) as they were wired by hand, in their order. opencode and codex shut down
// through Serve().Shutdown(), which their Shutdown wraps.
var handWiredLifecycle = []struct {
	kind      string
	reconcile func(string)
	abort     func()
	shutdown  func()
}{
	{"opencode", opencode.ReconcileManaged, opencode.AbortManaged, opencode.Shutdown},
	{"codex", codex.ReconcileManaged, codex.AbortManaged, codex.Shutdown},
	{"copilot", copilot.ReconcileManaged, copilot.AbortManaged, copilot.Shutdown},
	{"cursor", cursor.ReconcileManaged, cursor.AbortManaged, cursor.Shutdown},
	{"kiro", kiro.ReconcileManaged, kiro.AbortManaged, kiro.Shutdown},
	{"lcpp", lcpp.ReconcileManaged, lcpp.AbortManaged, lcpp.Shutdown},
	{"muse", muse.ReconcileManaged, muse.AbortManaged, muse.Shutdown},
}

func funcPtr(f any) uintptr { return reflect.ValueOf(f).Pointer() }

// TestManagedLifecycleMatchesHandWiring requires the capability on exactly the hand-wired kinds,
// each field to be the very function that kind's line called, and managedLifecycles to come out
// in the hand-wired order.
func TestManagedLifecycleMatchesHandWiring(t *testing.T) {
	var withCap []string
	for _, k := range kinds.All() {
		a, ok := agentreg.Lookup(k)
		if !ok {
			t.Fatalf("kind %q is in internal/kinds but not in the registry", k)
		}
		if _, ok := a.(agents.ManagedLifecycler); ok {
			withCap = append(withCap, k)
		}
	}
	var want []string
	for _, h := range handWiredLifecycle {
		want = append(want, h.kind)
	}
	if !reflect.DeepEqual(withCap, want) {
		t.Fatalf("kinds with ManagedLifecycle, in kinds order = %v, hand wiring = %v", withCap, want)
	}

	got := managedLifecycles()
	if len(got) != len(handWiredLifecycle) {
		t.Fatalf("managedLifecycles() has %d entries, want %d", len(got), len(handWiredLifecycle))
	}
	for i, h := range handWiredLifecycle {
		lc := got[i]
		if lc.Reconcile == nil || lc.Abort == nil || lc.Shutdown == nil {
			t.Fatalf("%s: a lifecycle field is nil: %+v", h.kind, lc)
		}
		if funcPtr(lc.Reconcile) != funcPtr(h.reconcile) {
			t.Errorf("entry %d (%s): Reconcile is not %s.ReconcileManaged", i, h.kind, h.kind)
		}
		if funcPtr(lc.Abort) != funcPtr(h.abort) {
			t.Errorf("entry %d (%s): Abort is not %s.AbortManaged", i, h.kind, h.kind)
		}
		if funcPtr(lc.Shutdown) != funcPtr(h.shutdown) {
			t.Errorf("entry %d (%s): Shutdown is not %s's shutdown", i, h.kind, h.kind)
		}
	}
}

// handWiredConnectionStatuses is the kind entries of handleConnectionsGet's map as they were
// wired by hand.
func handWiredConnectionStatuses(s *secrets.Data, ghConnected bool) map[string]any {
	return map[string]any{
		"claude":   claude.Status(),
		"opencode": opencode.Status(s),
		"codex":    codex.Status(),
		"cursor":   cursor.Status(),
		"kiro":     kiro.Status(),
		"agy":      agy.Status(),
		"muse":     muse.Status(),
		"lcpp":     lcppStatus(s),
		"copilot":  copilot.Status(ghConnected),
	}
}

// TestConnectionStatusMatchesHandWiring requires agentConnectionStatuses to give the hand-wired
// keys and values for inputs that move the kinds reading them: opencode's keys, the member's lcpp
// connection and copilot's github flag.
func TestConnectionStatusMatchesHandWiring(t *testing.T) {
	cases := []struct {
		name string
		s    *secrets.Data
		gh   bool
	}{
		{"empty", &secrets.Data{}, false},
		{"github connected", &secrets.Data{}, true},
		{"opencode key", &secrets.Data{Opencode: map[string]string{"ANTHROPIC_API_KEY": "x"}}, false},
		{"lcpp member conn", &secrets.Data{Lcpp: &secrets.LcppConn{URL: "http://127.0.0.1:1"}}, true},
		{"everything at once", &secrets.Data{Opencode: map[string]string{"OPENAI_API_KEY": "x"}, Lcpp: &secrets.LcppConn{URL: "http://127.0.0.1:1"}}, true},
	}
	moved := map[string]bool{}
	var last map[string]any
	for _, c := range cases {
		want := handWiredConnectionStatuses(c.s, c.gh)
		got := agentConnectionStatuses(agents.StatusInputs{Secrets: c.s, GithubConnected: c.gh})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, want)
		}
		if last != nil {
			for k := range want {
				if !reflect.DeepEqual(want[k], last[k]) {
					moved[k] = true
				}
			}
		}
		last = want
	}
	// The fixtures are only a proof for the inputs if they move the kinds that read them.
	for _, k := range []string{"opencode", "lcpp", "copilot"} {
		if !moved[k] {
			t.Errorf("no fixture changed %s's status: the inputs it reads are not exercised", k)
		}
	}
}
