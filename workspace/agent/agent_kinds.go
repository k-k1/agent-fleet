package main

import (
	"net/http"
	"slices"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
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
	// Access is what the member's launch screens may say about what is hidden (ADR 0138 D10a).
	// Live only.
	Access *memberAccessWire `json:"access,omitempty"`
}

// memberAccessWire is the member-facing summary behind the launch dialog's one line and the
// "hidden by your own settings" line. It is booleans only on purpose: a member's screens never
// name a layer or list a hidden item (D10a), so nothing here could.
type memberAccessWire struct {
	// NoRouteAvailable: no harness has a route the policy allows. The member is sent to an
	// administrator.
	NoRouteAvailable bool `json:"noRouteAvailable"`
	// AllOffByOwnSettings: the policy allows some harness, and the member's own switches have
	// turned every such harness off. The member is offered the reset, not sent to an
	// administrator.
	AllOffByOwnSettings bool `json:"allOffByOwnSettings"`
	// SomeOffByOwnSettings: the member's own switches hide at least one harness the policy
	// allows. A harness the policy forbids anyway never counts, so this cannot reveal one.
	SomeOffByOwnSettings bool `json:"someOffByOwnSettings"`
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
	// Routes are the model routes the harness's RouteAdapter declares (ADR 0128), sorted. Live
	// only, like InstructionDelivery: the Control Plane's own copy is agentroutes/catalog.json,
	// so the kind snapshot does not carry them twice. The Console offers a route picker only for
	// a kind that lists some.
	Routes []string `json:"routes,omitempty"`
	// FixedRoutes are the routes the kind runs on with no selection (agents.FixedRouter, ADR
	// 0138 D2), sorted. Live only, for the same reason as Routes.
	FixedRoutes []string `json:"fixedRoutes,omitempty"`
	// UsableRoutes are the routes, selectable or fixed, whose credential this Workspace holds now
	// (agents.CredentialResolver), sorted. Live only. Absent when the kind has no resolver or did
	// not answer within usableRoutesBudget (unknown, not "none"); an empty list is "none".
	UsableRoutes *[]string `json:"usableRoutes,omitempty"`
	// Chat says whether the kind's assistant chat backend is offered: true when it has one and the
	// policy allows one of the backend's routes. Absent for a kind with no chat backend. Live only.
	Chat *bool `json:"chat,omitempty"`
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
	var usable map[string][]string
	if live {
		usable = usableRoutesOf(kinds)
	}
	// The live catalog hides what the credential policy forbids (ADR 0138 D10a): a kind none of
	// whose routes is allowed is absent, and one with some allowed lists only those.
	// The member's own switches hide a kind the same way (the member layer, D7 layer 4).
	hide, hiding := routepolicy.Hiding()
	chatRoutes := chatx.ChatBackendRoutes()
	if live {
		a := memberAccessOf(kinds, hide, hiding, sessionx.KindSwitchedOff)
		out.Access = &a
	}
	for _, k := range kinds {
		if live && (hiding && hide.HidesKind(kindRoutesOf(k)) || sessionx.KindSwitchedOff(k)) {
			continue
		}
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
			if ro, ok := sessionx.AgentOf(k).(agents.RouteOfferer); ok {
				e.Routes = slices.Sorted(slices.Values(ro.OfferedRoutes()))
			} else if ra, ok := sessionx.AgentOf(k).(agents.RouteAdapter); ok {
				e.Routes = slices.Sorted(slices.Values(ra.Routes()))
			}
			if f := agents.FixedRoutesOf(sessionx.AgentOf(k)); len(f) > 0 {
				e.FixedRoutes = slices.Sorted(slices.Values(f))
			}
			if u, ok := usable[k]; ok {
				u = slices.Sorted(slices.Values(u))
				if hiding {
					u = hide.Filter(u)
				}
				if u == nil {
					u = []string{}
				}
				e.UsableRoutes = &u
			}
			if hiding {
				e.Routes, e.FixedRoutes = hide.Filter(e.Routes), hide.Filter(e.FixedRoutes)
			}
			if rs, ok := chatRoutes[k]; ok {
				offered := !hiding || !hide.HidesKind(rs)
				e.Chat = &offered
			}
		}
		out.Kinds = append(out.Kinds, e)
	}
	return out
}

// memberAccessOf folds the policy and the member's switches into memberAccessWire, over the kinds
// that run on a model (a kind with no route, shell or ssm, is not a harness here).
func memberAccessOf(kinds []string, hide routepolicy.Policy, hiding bool, switchedOff func(string) bool) memberAccessWire {
	allowed, on := 0, 0
	for _, k := range kinds {
		rs := kindRoutesOf(k)
		if len(rs) == 0 || hiding && hide.HidesKind(rs) {
			continue
		}
		allowed++
		if !switchedOff(k) {
			on++
		}
	}
	return memberAccessWire{
		NoRouteAvailable:     allowed == 0,
		AllOffByOwnSettings:  allowed > 0 && on == 0,
		SomeOffByOwnSettings: on < allowed,
	}
}

// kindRoutesOf is every route kind may run on: the selectable routes it offers or declares and the
// fixed ones it resolves to with no selection. Empty for a kind that runs no model.
func kindRoutesOf(kind string) []string {
	a, ok := sessionx.RegisteredAgent(kind)
	if !ok {
		return nil
	}
	var out []string
	if ro, ok := a.(agents.RouteOfferer); ok {
		out = append(out, ro.OfferedRoutes()...)
	} else if ra, ok := a.(agents.RouteAdapter); ok {
		out = append(out, ra.Routes()...)
	}
	return append(out, agents.FixedRoutesOf(a)...)
}

// usableRoutesBudget bounds how long the catalog waits for the kinds' UsableRoutes. Some read a
// cached CLI probe that is cold right after the Agent starts (cursor, kiro: seconds); the catalog
// is on the Console's boot path, so a kind that misses the budget is reported as unknown rather
// than holding the answer up.
var usableRoutesBudget = 2 * time.Second

// usableRoutesOf asks every kind with an agents.CredentialResolver for its usable routes, in
// parallel, and returns the answers that arrived within usableRoutesBudget.
func usableRoutesOf(kinds []string) map[string][]string {
	type answer struct {
		kind   string
		routes []string
	}
	ch := make(chan answer, len(kinds))
	n := 0
	for _, k := range kinds {
		a, ok := sessionx.RegisteredAgent(k)
		if !ok {
			continue
		}
		cr, ok := a.(agents.CredentialResolver)
		if !ok {
			continue
		}
		n++
		go func() { ch <- answer{k, cr.UsableRoutes()} }()
	}
	out := map[string][]string{}
	timeout := time.After(usableRoutesBudget)
	for ; n > 0; n-- {
		select {
		case a := <-ch:
			out[a.kind] = a.routes
		case <-timeout:
			return out
		}
	}
	return out
}

// instrSupport answers from instrSupportedKinds and agents.InstructionsUnsupported. A kind on
// neither (shell, ssm, or one just added) is unverified rather than "no": nobody has said.
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
