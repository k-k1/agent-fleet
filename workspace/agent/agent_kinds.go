package main

import (
	"net/http"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// GET /agents/kinds — the kind catalog (#1077): one entry per registered kind, built from the
// registry and each kind's Caps so the CP and the Console can derive their kind lists instead
// of hand-keeping copies that drift. Presentation (colour, icon, wording) stays with them.
//
// The static part is also checked in as control-plane/internal/agentkinds/catalog.json,
// which the CP embeds because it validates tenant definitions with no Workspace to ask;
// TestKindCatalogSnapshot keeps that file equal to this answer.

// kindCatalogWire is the response envelope.
type kindCatalogWire struct {
	Kinds []kindEntryWire `json:"kinds"`
}

// kindEntryWire is one kind. The tri-state fields carry "yes" / "no" / "unverified"
// (agents.Tri): a consumer must treat "unverified" as unknown, not as unsupported.
type kindEntryWire struct {
	Kind string `json:"kind"`
	// ManagedOnly: no Terminal (CLI) route at all (Caps.ManagedOnly).
	ManagedOnly bool `json:"managedOnly"`
	// ManagedDefault: the driver a caller that cannot drive a pane should ask for is
	// managed — ManagedOnly kinds included, since they have nothing else.
	ManagedDefault bool         `json:"managedDefault"`
	MCPClient      string       `json:"mcpClient"`
	Instructions   string       `json:"instructions"`
	Caps           kindCapsWire `json:"caps"`
	// InstructionDelivery is instrDeliveryOf's answer ("file" / "compose" / "config" /
	// "prompt"). Live only: opencode's depends on the installed major version, so the
	// checked-in snapshot leaves it out.
	InstructionDelivery string `json:"instructionDelivery,omitempty"`
	// InstructionsReason is instrUnsupportedOf's reason when Instructions is "no".
	InstructionsReason string `json:"instructionsReason,omitempty"`
}

// kindCapsWire is the boolean half of agents.Caps that endpoints already gate on.
type kindCapsWire struct {
	CanFork          bool `json:"canFork"`
	CanForkAt        bool `json:"canForkAt"`
	CanTranscript    bool `json:"canTranscript"`
	UsesLabel        bool `json:"usesLabel"`
	PermissionChoice bool `json:"permissionChoice"`
}

// buildKindCatalog assembles the catalog. live=false is the snapshot form: only what is fixed
// at build time, so the checked-in copy does not depend on the machine that generated it.
func buildKindCatalog(live bool) kindCatalogWire {
	kinds := sessionx.RegisteredKinds()
	out := kindCatalogWire{Kinds: make([]kindEntryWire, 0, len(kinds))}
	for _, k := range kinds {
		c := sessionx.AgentOf(k).Caps()
		e := kindEntryWire{
			Kind:           k,
			ManagedOnly:    c.ManagedOnly,
			ManagedDefault: sessionx.ManagedDefaultKind(k),
			MCPClient:      c.MCPClient.Wire(),
			Instructions:   instrSupport(k).Wire(),
			Caps: kindCapsWire{
				CanFork:          c.CanFork,
				CanForkAt:        c.CanForkAt,
				CanTranscript:    c.CanTranscript,
				UsesLabel:        c.UsesLabel,
				PermissionChoice: c.PermissionChoice,
			},
		}
		e.InstructionsReason = instrUnsupportedOf(k)
		if live {
			e.InstructionDelivery = instrDeliveryOf(k)
		}
		out.Kinds = append(out.Kinds, e)
	}
	return out
}

// instrSupport answers from instrSupportedKinds and agents.InstructionsUnsupported. A kind on neither (shell, ssm, or one
// just added) is unverified rather than "no": nobody has said.
func instrSupport(kind string) agents.Tri {
	if slices.Contains(instrSupportedKinds, kind) {
		return agents.TriYes
	}
	if instrUnsupportedOf(kind) != "" {
		return agents.TriNo
	}
	return agents.TriUnverified
}

func handleAgentKinds(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, buildKindCatalog(true))
}
