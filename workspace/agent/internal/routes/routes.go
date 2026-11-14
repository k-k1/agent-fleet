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
	// WireOpenAIImages: the OpenAI Images API (image generation on the deployment's engine).
	WireOpenAIImages Wire = "openai-images"
	// WireVendor: a CLI-locked login or key whose CLI speaks its vendor's own protocol; Agent
	// Fleet never sees the calls.
	WireVendor Wire = "vendor"
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
	// TierTenantKey: a credential an administrator configured once for the tenant: a key, or for
	// bedrock-tenant a cross-account role the tenant's account pays for (ADR 0129 decision 1).
	TierTenantKey Tier = "tenant-key"
	// TierDeployment: a route the deployment provides and pays for (the engine, bedrock-deployment),
	// which follows its own gates.
	TierDeployment Tier = "deployment"
)

// Class is a route's credential class (ADR 0138 D1): what kind of credential pays for its calls.
// The policy of ADR 0138 judges a route by its class and destination, never by the harness.
type Class string

const (
	// ClassSubscription: a plan or account login, CLI-locked or not.
	ClassSubscription Class = "subscription"
	// ClassMemberKey: a metered key the member holds.
	ClassMemberKey Class = "member-key"
	// ClassMemberCloud: the member's own cloud account (a marked AWS profile).
	ClassMemberCloud Class = "member-cloud"
	// ClassProvided: a credential the tenant or the deployment holds and pays for (ADR 0129).
	ClassProvided Class = "provided"
	// ClassSelfHosted: the deployment's engine, or an approved self-hosted endpoint.
	ClassSelfHosted Class = "self-hosted"
)

// Destination is who receives a route's prompts (ADR 0138 §2). It is what a data-residency rule
// is about, so a vendor key keeps its vendor's destination only while the harness pins the
// vendor's endpoint.
type Destination string

const (
	DestAnthropic  Destination = "anthropic"
	DestOpenAI     Destination = "openai"
	DestGoogle     Destination = "google"
	DestMeta       Destination = "meta"
	DestGitHub     Destination = "github"
	DestCursor     Destination = "cursor"
	DestKiro       Destination = "kiro"
	DestOpencodeAI Destination = "opencode.ai"
	DestOpenRouter Destination = "openrouter"
	DestSakana     Destination = "sakana"
	// DestAWSBedrock: Amazon Bedrock in an AWS account (the member's, the tenant's or the
	// deployment's; the class says whose).
	DestAWSBedrock Destination = "aws-bedrock"
	// DestAFEngine: this deployment's own inference engine.
	DestAFEngine Destination = "af-engine"
	// DestUnclassified: a destination Agent Fleet cannot vouch for. Wherever any restriction is
	// set it is refused (fail closed).
	DestUnclassified Destination = "unclassified"
)

// Classes is every class, in the order ADR 0138 D1 lists them.
func Classes() []Class {
	return []Class{ClassSubscription, ClassMemberKey, ClassMemberCloud, ClassProvided, ClassSelfHosted}
}

// Destinations is every destination, unclassified last.
func Destinations() []Destination {
	return []Destination{DestAnthropic, DestOpenAI, DestGoogle, DestMeta, DestGitHub, DestCursor, DestKiro,
		DestOpencodeAI, DestOpenRouter, DestSakana, DestAWSBedrock, DestAFEngine, DestUnclassified}
}

// Route ids. Existing ids keep their names; a new one names the credential, not the harness:
// <destination>-<subscription|api> for a vendor plan or key, <harness>-login only where the login
// exists for one harness and its plan is not a separable product (ADR 0138 D2).
const (
	RouteAFEngineLLM           = "af-engine-llm"
	RouteAFEngineImage         = "af-engine-image"
	RouteAgyLogin              = "agy-login"
	RouteAnthropicAPI          = "anthropic-api"
	RouteAnthropicSubscription = "anthropic-subscription"
	RouteBedrock               = "bedrock"
	RouteBedrockDeployment     = "bedrock-deployment"
	RouteBedrockTenant         = "bedrock-tenant"
	RouteChatGPTSubscription   = "chatgpt-subscription"
	RouteCopilotLogin          = "copilot-login"
	RouteCursorLogin           = "cursor-login"
	RouteCustomAPI             = "custom-api"
	RouteGeminiAPI             = "gemini-api"
	RouteKiroLogin             = "kiro-login"
	RouteMemberLlamacpp        = "member-llamacpp"
	RouteMetaAPI               = "meta-api"
	RouteMuseLogin             = "muse-login"
	RouteOpenAIAPI             = "openai-api"
	RouteOpencodeAccount       = "opencode-account"
	RouteOpencodeGo            = "opencode-go"
	RouteOpencodeZen           = "opencode-zen"
	RouteOpenRouterAPI         = "openrouter-api"
	RouteSakanaAPI             = "sakana-api"
	RouteVertex                = "vertex"
)

// Route is one catalogue entry.
type Route struct {
	ID         string `json:"id"`
	Wire       Wire   `json:"wire"`
	Credential Tier   `json:"credential"`
	// Class and Destination are what a policy judges (ADR 0138 D1c). Every route has exactly one
	// of each. For a PerConnection route they are what the route reads as while its connection's
	// endpoint is not an approved one: the endpoint the member set decides the real pair.
	Class       Class       `json:"class"`
	Destination Destination `json:"destination"`
	// PerConnection marks a route whose endpoint the member can set (a llama.cpp URL, a custom
	// opencode provider). Its class and destination come from the validated endpoint its
	// connection is bound to, so a per-route exception can never name it (ADR 0138 D1d).
	PerConnection bool `json:"perConnection,omitempty"`
}

// catalogue is sorted by id. The ids are the vocabulary the usage ledger already records
// (usagex.Route*); TestRouteAdaptersDeclareCataloguedRoutes holds the two together.
//
// The three Bedrock ids are one wire with three payers (ADR 0129 decision 1): bedrock is the
// member's own profile, bedrock-tenant and bedrock-deployment are the provided routes. They are
// separate ids so "who pays" is in every ledger rollup key without a new field. A provided id is
// catalogued before any harness declares it, so a selection naming one is refused like any route
// a harness does not accept.
//
// opencode-zen is member-key although its tier is existing-connection: Zen bills the key per call
// (a priced Zen call is metered, usagex.BillingOfOpencodeCall), while Go is a plan. opencode's
// free models are filed under opencode-zen by the ledger and follow its class and destination.
//
// member-llamacpp reads as member-key and unclassified until its endpoint is approved: the
// connection accepts any URL, so a vendor or a proxy entered there must not pass as self-hosted
// (ADR 0138 §2).
var catalogue = []Route{
	{ID: RouteAFEngineImage, Wire: WireOpenAIImages, Credential: TierDeployment, Class: ClassSelfHosted, Destination: DestAFEngine},
	{ID: RouteAFEngineLLM, Wire: WireOpenAIChat, Credential: TierDeployment, Class: ClassSelfHosted, Destination: DestAFEngine},
	{ID: RouteAgyLogin, Wire: WireVendor, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestGoogle},
	{ID: RouteAnthropicAPI, Wire: WireAnthropicMessages, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestAnthropic},
	{ID: RouteAnthropicSubscription, Wire: WireAnthropicMessages, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestAnthropic},
	{ID: RouteBedrock, Wire: WireBedrock, Credential: TierAWSProfile, Class: ClassMemberCloud, Destination: DestAWSBedrock},
	{ID: RouteBedrockDeployment, Wire: WireBedrock, Credential: TierDeployment, Class: ClassProvided, Destination: DestAWSBedrock},
	{ID: RouteBedrockTenant, Wire: WireBedrock, Credential: TierTenantKey, Class: ClassProvided, Destination: DestAWSBedrock},
	{ID: RouteChatGPTSubscription, Wire: WireOpenAIResponses, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestOpenAI},
	{ID: RouteCopilotLogin, Wire: WireVendor, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestGitHub},
	{ID: RouteCursorLogin, Wire: WireVendor, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestCursor},
	{ID: RouteCustomAPI, Wire: WirePerModel, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestUnclassified, PerConnection: true},
	{ID: RouteGeminiAPI, Wire: WirePerModel, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestGoogle},
	{ID: RouteKiroLogin, Wire: WireVendor, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestKiro},
	{ID: RouteMemberLlamacpp, Wire: WireOpenAIChat, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestUnclassified, PerConnection: true},
	{ID: RouteMetaAPI, Wire: WireVendor, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestMeta},
	{ID: RouteMuseLogin, Wire: WireVendor, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestMeta},
	{ID: RouteOpenAIAPI, Wire: WireOpenAIResponses, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestOpenAI},
	{ID: RouteOpencodeAccount, Wire: WirePerModel, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestOpencodeAI},
	{ID: RouteOpencodeGo, Wire: WirePerModel, Credential: TierExistingConnection, Class: ClassSubscription, Destination: DestOpencodeAI},
	{ID: RouteOpencodeZen, Wire: WirePerModel, Credential: TierExistingConnection, Class: ClassMemberKey, Destination: DestOpencodeAI},
	{ID: RouteOpenRouterAPI, Wire: WirePerModel, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestOpenRouter},
	{ID: RouteSakanaAPI, Wire: WirePerModel, Credential: TierMemberKey, Class: ClassMemberKey, Destination: DestSakana},
	{ID: RouteVertex, Wire: WireVertex, Credential: TierTenantKey, Class: ClassProvided, Destination: DestGoogle},
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
	// CodeRouteSettings: the route's credential or endpoint would be overridden by the CLI's own
	// settings (an env block or apiKeyHelper in a settings file), so the route cannot be honoured.
	CodeRouteSettings = "route_settings_conflict"
)

// Refusal codes of the switch away from a stopped subscription (decision 10).
const (
	// CodeSwitchUnsupported: the harness declares no route to switch to.
	CodeSwitchUnsupported = "route_switch_unsupported"
	// CodeSwitchNotSubscription: the session's route is not a subscription.
	CodeSwitchNotSubscription = "route_switch_not_subscription"
	// CodeSwitchUnproven: the session has no explicit selection and nothing proves it runs on the
	// subscription.
	CodeSwitchUnproven = "route_switch_route_unproven"
	// CodeSwitchNoLimit: there is no limit evidence on the session's current route.
	CodeSwitchNoLimit = "route_switch_no_limit"
	// CodeSwitchSpendLimit: the limit is a spend cap; waiting never clears it, and a metered route
	// is no answer to a billing decision.
	CodeSwitchSpendLimit = "route_switch_spend_limit"
	// CodeSwitchState: the session is working, has a question pending, or is moving.
	CodeSwitchState = "route_switch_state"
	// CodeSwitchBusy: a switch or a move of this session is already running or unfinished.
	CodeSwitchBusy = "route_switch_busy"
	// CodeSwitchFailed: the switch failed and was rolled back; the message says what state the
	// session is in.
	CodeSwitchFailed = "route_switch_failed"
	// CodeSwitchBooking: the usage-limit resume booking could not be recorded as cancelled, so
	// nothing was changed.
	CodeSwitchBooking = "route_switch_booking"
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

// AWSConnection is the Connection of a selection on an AWS-profile route (tier TierAWSProfile):
// the Settings profile's row id and the region the calls go to, "<profile id>@<region>". It is
// the stable reference decision 3 asks for: the id survives a rename, and replacing the profile
// with another row of the same name is another connection. It holds no secret. An empty region
// means "the profile's own region".
func AWSConnection(profileID, region string) string {
	return strings.TrimSpace(profileID) + "@" + strings.TrimSpace(region)
}

// ParseAWSConnection splits an AWSConnection, bound or not. ok is false for anything that is not
// one: a missing id, a second separator, characters outside what a row id and a region use, or a
// binding that is not a fingerprint.
func ParseAWSConnection(conn string) (profileID, region string, ok bool) {
	profileID, region, _, ok = ParseBoundAWSConnection(conn)
	return profileID, region, ok
}

// BindAWSConnection is the STORED form of an AWS connection: the profile and the region resolved
// when the session was created, and fp, the fingerprint of the profile's identity (account, role,
// sign-in, region) at that moment, "<profile id>@<region>#<fp>". A session keeps it for life, so
// an edit of the profile in Settings is refused at the next start instead of moving the session
// to another account (decision 3); the plain form only says which row to use. No secret.
func BindAWSConnection(profileID, region, fp string) string {
	return AWSConnection(profileID, region) + "#" + fp
}

// ParseBoundAWSConnection is ParseAWSConnection that also returns the binding ("" when unbound).
func ParseBoundAWSConnection(conn string) (profileID, region, fp string, ok bool) {
	base, fp, bound := strings.Cut(conn, "#")
	if bound && !awsFingerprint(fp) {
		return "", "", "", false
	}
	id, region, found := strings.Cut(base, "@")
	if !found || id == "" || !awsConnPart(id) || !awsConnPart(region) {
		return "", "", "", false
	}
	return id, region, fp, true
}

// awsFingerprint is a binding: 16 lower-case hex digits.
func awsFingerprint(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func awsConnPart(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
