// Package routes is the Agent's route catalogue and the selection type a session stores (ADR 0128
// decisions 2, 3 and 12). It is a leaf: session, agents and sessionx all import it, so it imports
// none of them.
//
// A route is where a harness's model calls go and with which credential. The catalogue says what
// routes exist; which harness may run on which is declared by the harness itself
// (agents.RouteAdapter), never here, because the allow-list is not a cartesian product.
//
// The Control Plane embeds a snapshot of this catalogue and of the harness allow-lists
// (control-plane/internal/agentroutes/catalog.json); workspace/agent TestRouteCatalogSnapshot keeps
// the two equal.
package routes

import (
	"fmt"
	"strings"
)

// Wire is the protocol a route speaks.
type Wire string

const (
	WireAnthropicMessages Wire = "anthropic-messages"
	WireOpenAIResponses   Wire = "openai-responses"
	WireOpenAIChat        Wire = "openai-chat"
	WireBedrock           Wire = "bedrock"
	WireVertex            Wire = "vertex"
	// WirePerModel: the harness picks the protocol per model (opencode's Zen and Go).
	WirePerModel Wire = "per-model"
)

// Tier says who owns a route's credential, in ADR 0069's layering. It does not say which
// credential: that is the selection's connection reference.
type Tier string

const (
	// TierExistingConnection: a login or connection the member already has (a subscription).
	TierExistingConnection Tier = "existing-connection"
	// TierMemberKey: a key the member pasted.
	TierMemberKey Tier = "member-key"
	// TierAWSProfile: an AWSProfileRef (profile and region).
	TierAWSProfile Tier = "aws-profile-ref"
	// TierTenantKey: a key an administrator configured once for the tenant.
	TierTenantKey Tier = "tenant-key"
	// TierDeployment: a route the deployment provides (the engine), which follows its own gates.
	TierDeployment Tier = "deployment"
)

// Route is one catalogue entry.
type Route struct {
	ID         string `json:"id"`
	Wire       Wire   `json:"wire"`
	Credential Tier   `json:"credential"`
}

// catalogue is sorted by id. The ids are the vocabulary the usage ledger already records
// (usagex.Route*); TestCatalogueCoversLedgerRoutes holds the two together.
var catalogue = []Route{
	{"af-engine-llm", WireOpenAIChat, TierDeployment},
	{"anthropic-api", WireAnthropicMessages, TierMemberKey},
	{"anthropic-subscription", WireAnthropicMessages, TierExistingConnection},
	{"bedrock", WireBedrock, TierAWSProfile},
	{"chatgpt-subscription", WireOpenAIResponses, TierExistingConnection},
	{"member-llamacpp", WireOpenAIChat, TierMemberKey},
	{"openai-api", WireOpenAIResponses, TierMemberKey},
	{"opencode-go", WirePerModel, TierExistingConnection},
	{"opencode-zen", WirePerModel, TierExistingConnection},
	{"vertex", WireVertex, TierTenantKey},
}

// Catalogue returns every route, sorted by id.
func Catalogue() []Route { return append([]Route(nil), catalogue...) }

// Lookup finds a route by its exact id.
func Lookup(id string) (Route, bool) {
	for _, r := range catalogue {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}

// Selection is a session's explicit route choice: the route and a stable reference to the
// connection it runs on. It is stored in session.Meta and rides the create request; nil means
// "keep today's resolution rules" and is the only state every existing session is in.
//
// Connection is opaque here: its shape belongs to the route's credential tier (a connection id,
// or for Bedrock the profile and region as AWSProfileRef carries them). It names a connection
// and never holds a secret. Empty is allowed for a route that has only one possible connection.
// When the connection is deleted, expires or is repointed the session stops visibly; it is never
// moved to another connection, because that would move its bill.
type Selection struct {
	Route      string `json:"route"`
	Connection string `json:"connection,omitempty"`
}

// Clone returns an independent copy, nil for nil, so a fork never aliases its source's meta.
func (s *Selection) Clone() *Selection {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// Normalize trims the selection's fields. Matching stays exact after that: "Anthropic-API" is
// refused, not read as a spelling of a route.
func (s *Selection) Normalize() *Selection {
	if s == nil {
		return nil
	}
	return &Selection{Route: strings.TrimSpace(s.Route), Connection: strings.TrimSpace(s.Connection)}
}

// Refusal codes, shared by the Agent's create/fork/switch answers and the Control Plane's
// static check so one mistake reads the same on every path.
const (
	CodeBadRoute         = "bad_route"
	CodeRouteUnsupported = "route_unsupported"
	CodeRouteModel       = "route_model_mismatch"
	CodeRouteForbidden   = "route_connection_forbidden"
	CodeRouteUnavailable = "route_connection_unavailable"
)

// CheckStatic is the part of the validation that needs no state: the selection names a route in
// the catalogue, and allowed (the harness's declared list, nil for a harness that declares none)
// contains it. The Control Plane runs the same function over its snapshot before it wakes a
// Workspace, and the Agent runs it again because the snapshot may be older than the Agent.
// A nil selection always passes.
func CheckStatic(kind string, sel *Selection, allowed []string) (code string, err error) {
	if sel == nil {
		return "", nil
	}
	sel = sel.Normalize()
	if sel.Route == "" {
		return CodeBadRoute, fmt.Errorf("route is required when a route selection is given")
	}
	if _, ok := Lookup(sel.Route); !ok {
		ids := make([]string, 0, len(catalogue))
		for _, r := range catalogue {
			ids = append(ids, r.ID)
		}
		return CodeBadRoute, fmt.Errorf("unknown route %q; known routes: %s", sel.Route, strings.Join(ids, ", "))
	}
	if kind == "" {
		kind = "claude"
	}
	if len(allowed) == 0 {
		return CodeRouteUnsupported, fmt.Errorf("kind %q does not accept an explicit route yet; leave the route empty to keep its default resolution", kind)
	}
	for _, id := range allowed {
		if id == sel.Route {
			return "", nil
		}
	}
	return CodeRouteUnsupported, fmt.Errorf("kind %q cannot run on route %q; it accepts: %s", kind, sel.Route, strings.Join(allowed, ", "))
}
