package main

// engine_def.go — the row type of the engine table and its pure predicates (split out of
// engines.go; no runtime state lives here).

import (
	"strings"
	"time"
)

// engineDef is one row of the table 60-engines wrote. Every field is declared by the stack
// rather than derived here (ADR 0053): the engine is asleep most of the time, so anything
// the CP would have to ask the engine for is something it cannot ask.
//
// ⚠️ What the engine LOADS is not here at all. The catalogue is the whole declaration (ADR 0072
// decision 1), and phase P6 retired the last of the stack's copies — the model ids, the window
// and the S3 key that were read once as a seed. A table written by an older stack still carries
// those fields; they are ignored, which is the point: a CloudFormation parameter must not
// overwrite an administrator's choice on every CP restart. Everything else here (service, URL,
// health, capacity provider, idle, deadline, mode) really is a property of the vessel and stays
// the stack's to declare.
type engineDef struct {
	Key string `json:"key"` // "llm" — the path segment, the log prefix, the settings prefix
	API string `json:"api"` // "chat" | "images" — see engineAPI* below
	// Lifecycle says who owns starting and stopping this engine. Empty — every row written
	// before ADR 0076 — is this deployment: an ECS service whose desired count the controller
	// moves. "external" is a row that is nothing but a URL, a ComfyUI on the operator's own
	// network, and it is DECLARED rather than inferred from an empty `service` (ADR 0053, and
	// decision 1: a field somebody forgot must not turn into "somebody else runs this").
	//
	// An external row is given no ECS adapter, no controller, no active set, no pending reader,
	// no GPU ladder and no uptime sampler — see newEngineRegistry.
	Lifecycle string `json:"lifecycle"`
	Service   string `json:"service"` // ECS service whose desired count moves
	// LaunchTemplate is the EC2 launch template this role's boxes are bought from (ADR 0077
	// decision 1), by id (lt-…) or by name. Empty = this role does not buy boxes: a Fargate
	// engine, or a row written by a stack from before ADR 0077 — and such a row still works,
	// it simply starts the engine the plain way and chooses no card.
	//
	// 🔴 It REPLACES `capacityProvider` / `spotCapacityProvider`, which are ignored wherever a
	// stack still writes them. Those named ECS Managed Instances capacity providers, and the
	// whole of ADR 0077 is that the CP buys the instance itself: there is no provider left to
	// name, and a CP that went on reading them would be addressing resources the migration
	// deletes (ADR 0077 decision 11).
	LaunchTemplate string `json:"launchTemplate"`
	URL            string `json:"url"`    // http://llm.af.internal:8080
	Health         string `json:"health"` // "/health"
	// WarmPath is where "are there weights in memory" is asked, when that is a DIFFERENT
	// question from "is it healthy". Empty (the image role, and any ADR 0071 table) means the
	// health check answers both. "/models" for the llm role: a llama.cpp router answers /health
	// with ok while holding nothing at all (ADR 0072 P1, measured), so warmth is read from each
	// model's `status.value` instead.
	WarmPath    string `json:"warmPath"`
	Provider    string `json:"provider"`    // "llamacpp" — the provider id a Workspace configures
	APIKeyParam string `json:"apiKeyParam"` // SSM SecureString the engine's own --api-key is in
	// Classes is the ladder of GPU rungs this role may buy, as the operator declared it
	// (ADR 0074 decision 1; the format is parseEngineClasses'). Empty — the shipped default —
	// means this role chooses no card and nothing here ever buys one.
	//
	// It sits with the vessel rather than with the catalogue because a rung IS the vessel: the
	// stack owns the launch template, and this only says which of the shapes the operator
	// blessed may be selected. WHICH one is selected is a stored setting (decision 2).
	Classes string `json:"classes"`
	// Offers is the same ladder with a purchase option on each rung (ADR 0075 decision 1):
	//
	//	id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour|buy
	//
	// EMPTY MEANS `Classes`, read as a list of on-demand offers. That is the whole migration
	// (ADR 0075, 移行の節): an existing ladder moves to `offers` without a character changing,
	// and a stack that has not been updated yet keeps working because the column it never wrote
	// defaults to `od`. Same shape of gate as ADR 0072 P6 put in front of `<role>ModelS3Key` —
	// a table read by a newer CP must not make a role disappear.
	Offers string `json:"offers"`
	// OfferBudgetSec is how long ONE offer's box is given to REGISTER WITH THE CLUSTER before it
	// is terminated and the next offer is tried (ADR 0077 decision 1).
	//
	// 🔴 The meaning shrank with the ADR. Under 0075 this timed "can this capacity provider
	// produce a box at all", which `CreateFleet` now answers synchronously; what is left to time
	// is the box's own boot. 0 = the default 300 seconds, which is more than ten times the 21
	// seconds ADR 0045 decision 22 measured from launch to ECS registration (77 s with a
	// home-baked AMI) — a multiple of a measurement, not an AWS figure.
	OfferBudgetSec   int    `json:"offerBudgetSec"`
	IdleSec          int    `json:"idleSec"`
	StartDeadlineSec int    `json:"startDeadlineSec"`
	Mode             string `json:"mode"` // the DEFAULT mode; a stored setting wins
	// origin says which of the CP's own sources synthesised this row: "" for a table row,
	// engineOriginEnv for AF_COMFY_URL, engineOriginPanel for the admin panel. Never in the JSON
	// table — an operator cannot declare it — and it decides where the row's bearer comes from
	// (engineComfyPanel's precedence: a panel URL never presents an environment key).
	origin string
}

// Where a synthesised image row came from (ADR 0076 decision 2 and its 2026-10-04 addendum).
const (
	engineOriginEnv   = "env"
	engineOriginPanel = "panel"
)

// The two lifecycles that are not this deployment's, and they are NOT the same question
// (ADR 0076 decision 1, ADR 0079 decision 1):
//
//   - external — the engine is reachable and NOBODY starts it: a ComfyUI on the operator's own
//     network. A request arriving while it is down fails at once, because waiting cannot help.
//   - remote — the engine belongs to another Agent Fleet, and THAT fleet starts it. The row is
//     otherwise identical; what differs is that waiting is now the right thing to do.
//
// Both are DECLARED and never inferred from the URL's shape (ADR 0053): a URL that happens to
// contain /engine/ must not silently change what a row means.
const (
	engineLifecycleExternal = "external"
	engineLifecycleRemote   = "remote"
)

// lifecycle is the declared lifecycle, folded. "" is this deployment's own ECS service.
func (d engineDef) lifecycle() string {
	return strings.ToLower(strings.TrimSpace(d.Lifecycle))
}

// external reports whether this row is reachable and nothing here can start it.
//
// 🔴 This is the NARROW of the two questions and the only one that produces an immediate failure.
// A remote row must answer FALSE here: the far fleet is what starts that engine, so the request is
// worth holding, and an answer of true would make a borrowed cold start fail every time
// (ADR 0076 decision 4 is about an engine nothing can start — see ensureStarted).
func (d engineDef) external() bool {
	return d.lifecycle() == engineLifecycleExternal
}

// remote reports whether this row is another Agent Fleet's gateway (ADR 0079 decision 1).
func (d engineDef) remote() bool {
	return d.lifecycle() == engineLifecycleRemote
}

// notManagedHere reports whether the vessel is somebody else's: no ECS service to move a desired
// count on, no controller, no ladder, nothing here to start or stop.
//
// 🔴 This is the predicate almost every branch actually wants, and getting it wrong is silent.
// ADR 0079's review counted nine call sites of the single `external()` this replaces: eight of
// them meant THIS one, and the ninth (warm) meant neither.
func (d engineDef) notManagedHere() bool {
	return d.external() || d.remote()
}

// offersSpec is the offer list this role declares. `offers` when the stack writes one, and the
// ADR 0074 ladder otherwise — every rung of which is an on-demand offer.
func (d engineDef) offersSpec() string {
	if s := strings.TrimSpace(d.Offers); s != "" {
		return s
	}
	return d.Classes
}

// engineOfferBudgetDefault is the default registration ceiling (ADR 0077 decision 1).
const engineOfferBudgetDefault = 300 * time.Second

// offerBudget is how long one offer is waited on.
func (d engineDef) offerBudget() time.Duration {
	if d.OfferBudgetSec > 0 {
		return time.Duration(d.OfferBudgetSec) * time.Second
	}
	return engineOfferBudgetDefault
}

type engineTable struct {
	Engines []engineDef `json:"engines"`
	// Ingest is deployment-wide rather than per engine: one Fargate task definition stages a
	// file for whichever role asked for it (ADR 0072 decision 6). Absent on a table written
	// before phase P4, which is why every field is checked before use rather than assumed.
	Ingest engineIngestDef `json:"ingest"`
}

// engineIngestDef is what the Control Plane needs to START an ingest and to say why one failed.
//
// Declared by the stack, like everything else here (ADR 0053): a `RunTask` needs a task
// definition, subnets and a security group, and the CP cannot ask CloudFormation for them at
// request time.
type engineIngestDef struct {
	TaskDef        string   `json:"taskDef"`
	Subnets        []string `json:"subnets"`
	SecurityGroups []string `json:"securityGroups"`
	// LogGroup is where the task writes. The exit code says a container failed; this says why
	// (a sha256 mismatch, a 401 on a gated repository, no space), and that sentence is what the
	// panel shows.
	LogGroup string `json:"logGroup"`
	// TokenSecret is the Secrets Manager secret the ingest task reads HF_TOKEN from. The stack
	// always creates it and always injects it, holding a sentinel until somebody registers a
	// token, so this is a place to WRITE and never a statement that a token exists (ADR 0072
	// decision 6 as revised: the DB is the record of truth, this is the carrying path).
	TokenSecret string `json:"tokenSecret"`
	// Bucket is where verified model objects live. It is carried in the table rather than
	// derived from a stack name, so the S3 port works unchanged across AWS and test deployments.
	Bucket string `json:"bucket"`
	// HasToken is what a pre-P5 stack declared: HF_TOKEN came from a CloudFormation parameter,
	// so the table itself knew whether a gated repository could be taken in. It survives because
	// the CP is upgraded before the stack is — on such a table TokenSecret is empty, nothing can
	// be registered, and this is the whole answer.
	HasToken bool `json:"hasToken"`
	// CivitaiTokenSecret is the Secrets Manager secret the ingest task reads CIVITAI_TOKEN
	// from — the same carrying-path shape as TokenSecret, for an unrelated account. Empty on a
	// stack that predates it, where there is nothing to write to and nothing can be registered.
	CivitaiTokenSecret string `json:"civitaiTokenSecret"`
}

func (d engineIngestDef) ok() bool {
	return strings.TrimSpace(d.TaskDef) != "" && len(d.Subnets) > 0
}

// The API families an engine can speak. It is declared by the stack rather than guessed from
// the key, and it decides two things that are otherwise invisible:
//
//   - which engines the Agent turns into an opencode provider. An image engine written into
//     opencode's config would put `sdcpp/sdxl-base-1.0` in the launch menu as something to
//     hold a conversation with;
//   - who counts the usage. A chat response carries `usage` and the gateway is the only party
//     that sees it; an image response carries pixels, which the Agent counts as tool.imagegen
//     when it stores the file (ADR 0071 decision 9, ADR 0069 decision 9). Counting both here
//     would put an `engine.image` row with no tokens in it next to the real one.
const (
	engineAPIChat   = "chat"
	engineAPIImages = "images"
)

// api is the engine's API family, defaulting to chat. The default matters: a table written by
// the P0 stack has no `api` field at all, and the CP is upgraded before the stack is.
func (d engineDef) api() string {
	if v := strings.TrimSpace(d.API); v != "" {
		return v
	}
	return engineAPIChat
}

// imageServableProviders is the images-API vocabulary THIS BUILD implements a client for (ADR
// 0083 decision 5): comfy's own wire shape, and the OpenAI Images API openai-compat speaks. A
// row naming anything else — most likely `sdcpp`, retired the same ADR — cannot be served by
// this software no matter what its lifecycle or mode say, and that used to be entirely silent:
// the Workspace's imagegen.EngineLookup simply never matched it, so generate_image vanished from
// tools/list with no error anywhere.
var imageServableProviders = map[string]bool{"comfy": true, "openai-compat": true}

// imageProviderServable answers whether THIS BUILD can serve a row's declared provider. Always
// true off the images API: the chat role's provider vocabulary (llamacpp and friends) is a
// different question this ADR does not touch.
func imageProviderServable(d engineDef) bool {
	return d.api() != engineAPIImages || imageServableProviders[strings.TrimSpace(d.Provider)]
}
