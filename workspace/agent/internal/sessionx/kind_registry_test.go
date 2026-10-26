package sessionx

import (
	"errors"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// mcpDefFor is the smallest definition mcpreg.Validate accepts, scoped to one kind, so the
// kind is the only thing that can make it fail.
func mcpDefFor(kind string) mcpreg.ServerDef {
	return mcpreg.ServerDef{Name: "s", Origin: mcpreg.OriginUser, Transport: mcpreg.TransportStdio,
		Command: "x", Kinds: []string{kind}}
}

func mcpKindAccepted(t *testing.T, kind string) bool {
	t.Helper()
	err := mcpreg.Validate(mcpDefFor(kind))
	var ve *mcpreg.ValidationError
	if err != nil && (!errors.As(err, &ve) || ve.Code != mcpreg.CodeKindUnknown) {
		t.Fatalf("Validate(%q) failed for a reason other than the kind: %v", kind, err)
	}
	return err == nil
}

// The registry key is what every list and the wire spell; an agent answering a different
// Kind() is the alternate-spelling drift a derived list inherits silently.
func TestRegisteredKindsSpellTheirOwnKind(t *testing.T) {
	kinds := RegisteredKinds()
	if len(kinds) != len(agentRegistry) || !slices.IsSorted(kinds) {
		t.Fatalf("RegisteredKinds() = %v, want all %d registry keys sorted", kinds, len(agentRegistry))
	}
	for _, k := range kinds {
		if got := agentRegistry[k].Kind(); got != k {
			t.Errorf("agentRegistry[%q].Kind() = %q", k, got)
		}
		c := agentRegistry[k].Caps()
		if c.ManagedOnly && c.ManagedDefault {
			t.Errorf("%s: ManagedOnly and ManagedDefault both set; ManagedOnly already has no other driver", k)
		}
	}
}

// TestMCPClientKindsMatchRegistry is the drift guard behind mcpreg.knownKinds / ServedKinds:
// a kind whose Caps say it runs an MCP client and that mcpreg refuses (or the reverse) is the
// failure #1075 shipped, found only when an admin's definition was refused.
func TestMCPClientKindsMatchRegistry(t *testing.T) {
	for _, k := range RegisteredKinds() {
		mc := agentRegistry[k].Caps().MCPClient
		if mc == agents.TriUnverified {
			t.Errorf("%s: Caps().MCPClient is unverified; measure it and set yes or no", k)
			continue
		}
		want := mc == agents.TriYes
		if got := slices.Contains(mcpreg.ServedKinds, k); got != want {
			t.Errorf("%s: Caps().MCPClient=%s but in mcpreg.ServedKinds=%v", k, mc.Wire(), got)
		}
		if got := mcpKindAccepted(t, k); got != want {
			t.Errorf("%s: Caps().MCPClient=%s but mcpreg.Validate accepts it=%v", k, mc.Wire(), got)
		}
	}
	for _, k := range mcpreg.ServedKinds {
		if _, ok := agentRegistry[k]; !ok {
			t.Errorf("mcpreg.ServedKinds has %q, which is not a registered kind", k)
		}
	}
}

// Deriving a list is where alternate spellings used to slip in or fall out. None of these is
// a kind: the binary names, the provider id and case/space variants must stay refused, so a
// definition never lands on a kind nobody serves.
func TestMCPKindAlternateSpellingsRefused(t *testing.T) {
	for _, k := range []string{"", "Claude", "CLAUDE", " claude", "claude ", "claude-code",
		"llamacpp", "llama.cpp", "antigravity", "kiro-cli", "cursor-agent", "github-copilot", "Muse"} {
		if mcpKindAccepted(t, k) {
			t.Errorf("mcpreg.Validate accepts %q as a kind", k)
		}
	}
}

// A writer and a MaterializedKinds entry go together, for every REGISTERED kind: one added
// without the other is either skipped by MaterializeAll for ever or never run. Walking the real
// registry (not a copy of its names) is what catches a kind registered later.
func TestMaterializedKindsMatchWritersForEveryRegisteredKind(t *testing.T) {
	for _, k := range RegisteredKinds() {
		if has, listed := mcpreg.HasWriter(k), slices.Contains(mcpreg.MaterializedKinds, k); has != listed {
			t.Errorf("%s: has an MCP config writer=%v, in mcpreg.MaterializedKinds=%v", k, has, listed)
		}
	}
	for _, k := range mcpreg.MaterializedKinds {
		if _, ok := agentRegistry[k]; !ok {
			t.Errorf("mcpreg.MaterializedKinds has %q, which is not a registered kind", k)
		}
	}
}
