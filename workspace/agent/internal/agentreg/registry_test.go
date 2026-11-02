package agentreg

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
	kinds := Kinds()
	if len(kinds) != len(registry) || !slices.IsSorted(kinds) {
		t.Fatalf("Kinds() = %v, want all %d registry keys sorted", kinds, len(registry))
	}
	for _, k := range kinds {
		if got := registry[k].Kind(); got != k {
			t.Errorf("registry[%q].Kind() = %q", k, got)
		}
		c := registry[k].Caps()
		if c.ManagedOnly && c.ManagedDefault {
			t.Errorf("%s: ManagedOnly and ManagedDefault both set; ManagedOnly already has no other driver", k)
		}
	}
}

// TestMCPClientKindsMatchRegistry is the drift guard behind mcpreg.knownKinds / ServedKinds:
// a kind whose Caps say it runs an MCP client and that mcpreg refuses (or the reverse) is the
// failure #1075 shipped, found only when an admin's definition was refused.
func TestMCPClientKindsMatchRegistry(t *testing.T) {
	for _, k := range Kinds() {
		mc := registry[k].Caps().MCPClient
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
		if _, ok := registry[k]; !ok {
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

// An MCP config writer is exactly what separates a kind that gets a native config file from one
// that is served some other way. Every registered kind lands in one of three places, walked from
// the real registry (not a copy of its names) so a kind registered later is caught: a writer, a
// listed non-file route (muse on the wire, lcpp in process), or no MCP client at all.
func TestMCPWriterMatchesMCPClientForEveryRegisteredKind(t *testing.T) {
	servedWithoutFile := map[string]bool{"muse": true, "lcpp": true}
	for _, k := range Kinds() {
		a, _ := Lookup(k)
		client := a.Caps().MCPClient == agents.TriYes
		switch has := mcpreg.HasWriter(k); {
		case has && !client:
			t.Errorf("%s: has an MCP config writer but Caps().MCPClient is not yes", k)
		case !has && client && !servedWithoutFile[k]:
			t.Errorf("%s: MCPClient yes but no config writer and not listed as served without a file", k)
		case has && servedWithoutFile[k]:
			t.Errorf("%s: listed as served without a file but has a config writer", k)
		}
	}
	for _, k := range mcpreg.MaterializedKinds() {
		if _, ok := registry[k]; !ok {
			t.Errorf("mcpreg.MaterializedKinds() has %q, which is not a registered kind", k)
		}
	}
}
