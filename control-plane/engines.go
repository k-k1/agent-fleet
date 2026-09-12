package main

// engines.go — the table of self-hosted inference engines, and the per-engine runtime the
// gateway and the controller share (ADR 0071 P0).
//
// The table is written by the 60-engines stack into ONE SSM parameter and read here once at
// startup. It is a parameter rather than a set of environment variables because 30-ingress
// has about 9 KB of CloudFormation template budget left and six knobs per engine would eat
// a third of it (ADR 0071 decision 8); the CP task role's SSM read is already scoped to
// /af-ws/*, so the name has to live under that prefix and nothing new is granted.
//
// Read ONCE, at startup, on purpose: an engine's service name and URL only change when the
// stack changes, and a stack change replaces the CP task anyway. Re-reading it per request
// would put an SSM call in front of every token, which is a rate limit waiting to happen.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
}

// engineLifecycleExternal is the one lifecycle that is not this deployment's (ADR 0076
// decision 1): the engine is reachable and nothing here may start or stop it.
const engineLifecycleExternal = "external"

// external reports whether this row's lifecycle belongs to somebody else.
func (d engineDef) external() bool {
	return strings.EqualFold(strings.TrimSpace(d.Lifecycle), engineLifecycleExternal)
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
	// HasToken is what a pre-P5 stack declared: HF_TOKEN came from a CloudFormation parameter,
	// so the table itself knew whether a gated repository could be taken in. It survives because
	// the CP is upgraded before the stack is — on such a table TokenSecret is empty, nothing can
	// be registered, and this is the whole answer.
	HasToken bool `json:"hasToken"`
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

// engineRuntimeState is one engine, fully wired: the ECS adapter, its controller, its
// demand counter and the key it presents upstream.
type engineRuntimeState struct {
	def  engineDef
	ecs  *engineECS
	ctrl *engineController
	// settings is where the mode lives. Held here rather than reached through ctrl, which is
	// how it started: "what mode is this engine in" is a question about the engine, and making
	// the answer depend on whether a controller happens to exist means an admin toggle writes
	// a setting that nothing reads on any deployment that has no loop running. May be nil.
	settings store.SettingsStore
	demand   *engineDemand
	apiKey   string // llama-server's --api-key, read from SSM at startup; "" = the engine has none
	// catalog is what this engine may load (ADR 0072). Never nil, but its store may be — a CP
	// with no database answers "there are models" rather than stopping every engine.
	catalog *engineCatalog
	// ssm and activeParam are how the BOX is told what to load. Both empty on a dev CP with an
	// inline table: nothing is reading the parameter there.
	ssm         engineSSMWriteAPI
	activeParam string
	// pending is the other direction of the same conversation: what the INSTANCE says it has
	// not synced yet (ADR 0072 P2 欠落 7). nil on a CP with no AWS, and nil is "unknown",
	// which never holds a request.
	pending *enginePending
	served  engineServed
	// classes is the GPU ladder (ADR 0074), which ADR 0075 turned into the offer list. Empty on
	// every deployment that declares none, and then nothing in engine_class.go ever runs.
	//
	// 🔴 Read through classList() and written through setClasses(): this is the one part of
	// the engine table a running CP re-reads (engine_table_reload.go), so it changes under
	// the controller, the gateway and the admin panel while they are looking at it.
	classesMu sync.RWMutex
	classes   []engineClass
	cluster   string
	// fleet is how this role buys, finds and ends its box (ADR 0077). nil on a CP with no AWS,
	// on a Fargate engine, and on a row whose stack has not been migrated to a launch template —
	// and nil is what makes "not one EC2 call" structural for all three.
	fleet *engineFleet
	// startMu serialises the buy. Two start paths can reach it within a second of each other —
	// the admin toggle starts the box itself because somebody is watching, and the controller's
	// next tick agrees — and two CreateFleet calls a second apart are two GPUs.
	startMu sync.Mutex
	// offers is the state of rule 2 for the demand being served right now (ADR 0075 decision 5):
	// which offer the strategy was last written to, when, and what every earlier one answered.
	// Never nil, and inert for an engine that declares no offer — nothing consults it.
	offers *engineOfferRun
	// audit is where "we moved to another offer" is written down. The controller has its own
	// handle on the same ledger; this one is here because the move is decided by the engine's
	// offer machinery rather than by the controller's judgement, and "why is this running on the
	// expensive box" has to be answerable afterwards.
	audit engineAuditor
	// appliedMu guards the swap wait below. It is the last of what ADR 0074 kept in memory about
	// the card: the rung this process had written to a capacity provider went with the provider
	// itself (ADR 0077 decision 8 — the declaration is the request now).
	appliedMu sync.Mutex
	// extWarm is the cached health answer an externally managed engine's panel row reports as
	// `warm` (ADR 0076 decision 8). Inert for every engine that has a controller.
	extWarm engineWarmCache
	// swapWaitSince is when this process first refused to start because a box of the previous
	// rung was still registered. It bounds that wait (engineClassSwapWaitMax): the end of the
	// wait belongs to AWS, and `scaleInAfter: -1` would otherwise make it never end.
	swapWaitSince time.Time
}

// engineServed is which model this engine last answered with, and how often that changed.
//
// It exists because ADR 0072 decision 3 refuses to hide the price of `--models-max 1`: two
// sessions using two models take turns, and every turn costs an unload plus 267 seconds of
// weights going back into VRAM. A panel that showed only "warm" would show a healthy engine
// while every answer paid for a reload.
//
// In memory and nowhere else, like the demand window's buckets: a CP replaced a minute ago
// reports zero swaps while somebody is mid-conversation. The panel labels it accordingly rather
// than pretending the number spans the engine's life.
type engineServed struct {
	mu    sync.Mutex
	model string
	swaps int
}

// noteServed records the model an answer actually came back as. Only successful answers count:
// a 400 for a model the router does not hold did not move any weights.
func (e *engineRuntimeState) noteServed(model string, ok bool) {
	model = strings.TrimSpace(model)
	if model == "" || !ok {
		return
	}
	e.served.mu.Lock()
	defer e.served.mu.Unlock()
	if e.served.model != "" && e.served.model != model {
		e.served.swaps++
		log.Printf("%s: served model changed %s -> %s (%d swap(s) since this CP started)",
			e.def.Key, e.served.model, model, e.served.swaps)
	}
	e.served.model = model
}

// servedModel is the last model to answer, and the number of changes seen. The model is
// reported as "" once the engine is not warm — the box went away and took the weights with it,
// so naming one would tell the panel a request is cheap when it is a cold start.
func (e *engineRuntimeState) servedModel() (string, int) {
	e.served.mu.Lock()
	defer e.served.mu.Unlock()
	if e.ctrl != nil && !e.ctrl.warmed() {
		return "", e.served.swaps
	}
	return e.served.model, e.served.swaps
}

// engineRegistry is every engine this deployment runs. Nil (or empty) is the normal case —
// self-hosted inference is opt-in and a GPU box is $1.26/hour — and every entry point
// checks for it rather than assuming.
type engineRegistry struct {
	mu      sync.RWMutex
	byKey   map[string]*engineRuntimeState
	signKey []byte
	// ingest is deployment-wide (one task definition serves both roles), so it hangs off the
	// registry rather than off an engine. Nil when the stack declares none — a deployment
	// running an ADR 0071 table, or one whose CP has no AWS at all.
	ing *engineIngester
}

// ingester is the ingest runner, or nil when this deployment has none.
func (r *engineRegistry) ingester() *engineIngester {
	if r == nil {
		return nil
	}
	return r.ing
}

// ingestDef is what the stack declared about taking models in. The zero value is a complete
// answer: `hasToken` false and `ok()` false mean "no gated repositories, no ingest".
func (r *engineRegistry) ingestDef() engineIngestDef {
	if r == nil || r.ing == nil {
		return engineIngestDef{}
	}
	return r.ing.def
}

func (r *engineRegistry) get(key string) *engineRuntimeState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[key]
}

// list returns the engines in a stable order, for the launch-menu answer.
func (r *engineRegistry) list() []*engineRuntimeState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*engineRuntimeState, 0, len(r.byKey))
	for _, key := range []string{"llm", "image", "comfy"} {
		if e := r.byKey[key]; e != nil {
			out = append(out, e)
		}
	}
	for k, e := range r.byKey {
		if k != "llm" && k != "image" && k != "comfy" {
			out = append(out, e)
		}
	}
	return out
}

// engineSettingsFor names an engine's settings rows. VOICEVOX keeps the names ADR
// 0070 shipped (ttsEngineSettings); everything since is prefixed by its key.
func engineSettingsFor(key string) engineSettings {
	return engineSettings{
		mode:     "engine_" + key + "_mode",
		modeAt:   "engine_" + key + "_mode_at",
		demandAt: "engine_" + key + "_demand_at",
		negative: "engine_" + key + "_negative",
	}
}

// engineControlCfgFor is the controller tuning for one engine. Two of the values come from
// the stack rather than the environment, because they are properties of the hardware that
// stack bought: a start deadline shorter than the real cold start records every start as a
// failure and doubles the cooldown away (measured cold start from S3: 527 s, against ADR
// 0070's 300 s default), and the idle window is what a $1.26/hour box is worth.
//
// startUnits is 1: for an inference engine the request IS the demand (ADR 0071 decision 5).
// There is no Polly standing in while it starts, so there is no reason to wait for a second
// request before believing the first one.
func engineControlCfgFor(d engineDef) engineControlCfg {
	deadline := time.Duration(d.StartDeadlineSec) * time.Second
	if deadline <= 0 {
		deadline = 900 * time.Second
	}
	idle := time.Duration(d.IdleSec) * time.Second
	if d.IdleSec == 0 {
		idle = 1800 * time.Second
	}
	up := strings.ToUpper(d.Key)
	return engineControlCfg{
		interval:   time.Duration(runtime.EnvInt("AF_ENGINE_"+up+"_CONTROL_INTERVAL_SEC", 30)) * time.Second,
		window:     time.Duration(runtime.EnvInt("AF_ENGINE_"+up+"_WINDOW_SEC", 300)) * time.Second,
		startUnits: 1,
		idle:       time.Duration(runtime.EnvInt("AF_ENGINE_"+up+"_IDLE_SEC", int(idle.Seconds()))) * time.Second,
		deadline:   time.Duration(runtime.EnvInt("AF_ENGINE_"+up+"_START_DEADLINE_SEC", int(deadline.Seconds()))) * time.Second,
		cooldown:   time.Duration(runtime.EnvInt("AF_ENGINE_"+up+"_FAIL_COOLDOWN_SEC", 900)) * time.Second,
		// No undo window, even though there IS a toggle now (the admin panel of ADR 0071
		// P1.5). The grace exists to make an accidental OFF→ON cheap, and for VOICEVOX it is:
		// a 2 GB pull and 80 seconds. Here it would mean paying $1.26/hour for a box nobody
		// may ask for again, so the engine's own cold start is the price of changing your
		// mind (ADR 0070 decision 5, and why this one departs from it).
		offGrace: 0,
	}
}

// engineSSMAPI is the narrow SSM port, so a test can answer with a table of its own.
type engineSSMAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// loadEngineTable reads the table. AF_ENGINES_JSON is the inline form, for a dev CP with no
// AWS at all; AF_ENGINES_SSM_PARAM is what a real deployment is given.
func loadEngineTable(ctx context.Context, api engineSSMAPI) (engineTable, error) {
	if raw := strings.TrimSpace(envx.Or("AF_ENGINES_JSON", "")); raw != "" {
		return parseEngineTable(raw)
	}
	name := strings.TrimSpace(envx.Or("AF_ENGINES_SSM_PARAM", ""))
	if name == "" || api == nil {
		return engineTable{}, nil
	}
	out, err := api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)})
	if err != nil {
		return engineTable{}, fmt.Errorf("reading %s: %w", name, err)
	}
	return parseEngineTable(aws.ToString(out.Parameter.Value))
}

func parseEngineTable(raw string) (engineTable, error) {
	var t engineTable
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return engineTable{}, fmt.Errorf("parsing the engine table: %w", err)
	}
	for i, d := range t.Engines {
		if d.Key == "" || d.URL == "" {
			return engineTable{}, fmt.Errorf("engine %d: key and url are both required", i)
		}
		// A service is the thing a desired count moves on, so only a row this deployment
		// manages owes one (ADR 0076 decision 1). It stays required for every other row: an
		// empty service name would leave the controller driving nothing while the launch menu
		// still offered the model.
		if d.Service == "" && !d.external() {
			return engineTable{}, fmt.Errorf("engine %d: service is required unless lifecycle is %q", i, engineLifecycleExternal)
		}
	}
	return t, nil
}

// engineBoxRole is the `af-role` attribute value this row's boxes carry, "" for a row that has
// none (ADR 0077 decision 3). Derived from the key and the launch template together: a role that
// buys no box has no box to recognise, and an engine that claimed container instances it never
// bought would be reading the other role's GPU — or a workspace slot — as its own.
func engineBoxRole(d engineDef) string {
	if strings.TrimSpace(d.LaunchTemplate) == "" {
		return ""
	}
	return engineRoleAttr(d.Key)
}

// engineSubnets is where a box may be launched: the deployment's private subnets, exactly as the
// workspace side is given them (ADR 0077 P1 — 30-ingress passes AF_ECS_SUBNETS on every flavour,
// so nothing new has to be declared for this).
func engineSubnets() []string {
	var out []string
	for _, s := range strings.Split(envx.Or("AF_ECS_SUBNETS", ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseEngineOffers reads one role's offer list (ADR 0075 decision 1's format), with ADR 0077
// decision 9's refusal on top: THE LLM ROLE NEVER BUYS SPOT.
//
// ADR 0075 had a second safeguard — no Spot capacity provider was created for that role, so a
// `spot` row could not be addressed even if somebody wrote one. Nothing stands in the way here:
// one launch template serves both purchase options, and `DefaultTargetCapacityType` is a field
// the CP fills in. So the row is dropped at parse time, with a line saying so, and an operator
// who wanted Spot for a conversation finds out from the log rather than from a lost conversation.
func parseEngineOffers(key, spec string) []engineClass {
	list := parseEngineClasses(spec)
	if key != "llm" {
		return list
	}
	out := make([]engineClass, 0, len(list))
	for _, c := range list {
		if c.buy() == engineBuySpot {
			log.Printf("engines: llm: ignoring the offer %s: the llm role is on-demand only (a lost conversation is not a retry)", c.ID)
			continue
		}
		out = append(out, c)
	}
	return out
}

// engineComfyEnvRow synthesises the engine table row an operator gets from AF_COMFY_URL, plus
// the optional bearer from AF_COMFY_API_KEY (ADR 0076 decision 2).
//
// One variable, shaped like AF_VOICEVOX_URL, because that is the whole operator-facing surface
// of an engine somebody else runs. ComfyUI has no authentication of its own, so the key is
// there for a reverse proxy in front of it to check — and BOTH dial and engineHealthy present
// it, which means that proxy has to let the health path through with the same bearer or this
// engine is never healthy (decision 7).
//
// Read once, at startup: an environment variable cannot change under a running process, so
// changing the URL is a Control Plane restart.
func engineComfyEnvRow() (engineDef, string, bool) {
	url := strings.TrimSpace(envx.Or("AF_COMFY_URL", ""))
	if url == "" {
		return engineDef{}, "", false
	}
	return engineDef{
		Key:       "image",
		API:       engineAPIImages,
		Provider:  "comfy",
		URL:       url,
		Health:    "/system_stats",
		Lifecycle: engineLifecycleExternal,
	}, strings.TrimSpace(envx.Or("AF_COMFY_API_KEY", "")), true
}

// engineTableWithEnvRow merges the synthesised row into the table on the key both claim
// (ADR 0076 decision 2).
//
// A MANAGED row wins over the environment, which is the opposite of what the draft said. The
// review turned it round: replacing a controlled row leaves the ECS service it named with
// nobody to stop it, and paying for a GPU box nothing can switch off is the more expensive of
// the two mistakes. An operator moving that role onto a LAN box takes it out of the stack.
// Either way one line says which row won, because the alternative is a URL in the panel that
// matches neither of the two places it could have come from.
func engineTableWithEnvRow(rows []engineDef, env engineDef) []engineDef {
	for i, d := range rows {
		if d.Key != env.Key {
			continue
		}
		if !d.external() {
			log.Printf("engines: %s is a managed row in the engine table, so AF_COMFY_URL is ignored (take the role out of the stack to move it onto the network)", d.Key)
			return rows
		}
		log.Printf("engines: %s comes from AF_COMFY_URL (%s), replacing the external row in the engine table", env.Key, env.URL)
		out := append([]engineDef(nil), rows...)
		out[i] = env
		return out
	}
	log.Printf("engines: %s comes from AF_COMFY_URL (%s), externally managed", env.Key, env.URL)
	return append(append([]engineDef(nil), rows...), env)
}

// engineTableNeedsAWS reports whether any row in this table has to be driven through ECS. It
// is what keeps a native or docker deployment — nothing but AF_COMFY_URL, no credentials, no
// region — from loading an AWS config for a feature it is not using (ADR 0076 decision 3).
func engineTableNeedsAWS(t engineTable) bool {
	for _, d := range t.Engines {
		if !d.external() {
			return true
		}
	}
	return false
}

// newEngineRegistry builds and starts the engines. A failure to read the table is logged
// and leaves the registry empty rather than stopping the CP: the deployment's Workspaces,
// sessions and everything else do not depend on an engine existing, and refusing to boot
// over an optional feature is the larger outage.
func newEngineRegistry(ctx context.Context, mgr *manager) *engineRegistry {
	name := strings.TrimSpace(envx.Or("AF_ENGINES_SSM_PARAM", ""))
	inline := strings.TrimSpace(envx.Or("AF_ENGINES_JSON", ""))
	envRow, envAPIKey, hasEnvRow := engineComfyEnvRow()
	if name == "" && inline == "" && !hasEnvRow {
		return nil
	}
	// The inline table is parsed BEFORE any AWS client exists, because whether a single row
	// needs one is what decides whether a config is loaded at all (ADR 0076 decision 3).
	table := engineTable{}
	if inline != "" {
		t, err := parseEngineTable(inline)
		if err != nil {
			log.Printf("engines: disabled (%v)", err)
			return nil
		}
		table = t
	}
	var (
		ac      aws.Config
		ssmc    *ssm.Client
		ecsc    *ecs.Client
		ec2c    *ec2.Client
		haveAWS bool
	)
	if name != "" || engineTableNeedsAWS(table) {
		region := firstEnv("AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
		cfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
		if err != nil {
			log.Printf("engines: disabled (aws config: %v)", err)
			return nil
		}
		ac, haveAWS = cfg, true
		ssmc, ecsc = ssm.NewFromConfig(ac), ecs.NewFromConfig(ac)
		// 🔴 On EVERY flavour, not only ecs-ec2 (ADR 0077 P1): until this ADR the only EC2
		// client in the Control Plane was the slot pool's, built by the ecs-ec2 runtime alone,
		// and an engine runs on a deployment that has no slot pool at all. Constructing a client
		// makes no call and costs nothing; what decides whether EC2 is ever ASKED anything is
		// the launch template in the table row.
		ec2c = ec2.NewFromConfig(ac)
		if inline == "" {
			t, err := loadEngineTable(ctx, ssmc)
			if err != nil {
				log.Printf("engines: disabled (%v)", err)
				return nil
			}
			table = t
		}
	}
	if hasEnvRow {
		table.Engines = engineTableWithEnvRow(table.Engines, envRow)
	}
	if len(table.Engines) == 0 {
		return nil
	}

	var settings store.SettingsStore
	var auditor engineAuditor
	var models store.EngineModelStore
	if mgr != nil && mgr.store != nil {
		settings = mgr.store
		auditor = mgr.store
		models = mgr.store
	}
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}}
	if mgr != nil {
		reg.signKey = engineSignKey(mgr.tokenSignMaster())
	}
	cluster := firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")
	// Taking models in (ADR 0072 decision 6). Only wired when the stack declared an ingest task
	// AND there is somewhere to keep the jobs: without either, the panel says so and the manual
	// route stays. The reconcile loop is started here rather than per engine — one task
	// definition serves both roles.
	if haveAWS && mgr != nil && mgr.store != nil && table.Ingest.ok() {
		reg.ing = &engineIngester{
			def: table.Ingest, cluster: cluster, ecs: ecsc,
			logs:   newEngineIngestLogs(ac),
			store:  mgr.store,
			models: mgr.store,
			tokens: newEngineHfTokens(table.Ingest, mgr.store, mgr, secretsmanager.NewFromConfig(ac)),
			onDone: func(role string) {
				if e := reg.get(role); e != nil {
					e.catalog.invalidate()
					if err := e.publishActiveSet(context.Background()); err != nil {
						log.Printf("engines: %v", err)
					}
				}
			},
		}
		go reg.ing.run(context.Background())
	}
	for _, d := range table.Engines {
		if d.external() {
			// Everything an external row does NOT get (ADR 0076 decision 1): no ECS adapter, no
			// controller, no demand counter, no active set, no pending reader, no GPU ladder and
			// no uptime sampler. The catalogue and the settings store stay, because what may be
			// generated and whether the route is open are still this deployment's answers.
			st := &engineRuntimeState{
				def:      d,
				settings: settings,
				catalog:  newEngineCatalog(models, d.Key),
			}
			// The bearer the reverse proxy in front of ComfyUI checks (decision 7). Same field a
			// managed row fills from SSM, so dial and engineHealthy already present it; SSM is
			// never read for an external row, which is the point of the whole lane.
			if hasEnvRow && d == envRow {
				st.apiKey = envAPIKey
			}
			reg.byKey[d.Key] = st
			// No `service=`: there is none, and printing an empty one reads as a truncated line.
			log.Printf("engines: %s (%s) -> %s (external, health=%s models=%s)",
				d.Key, d.api(), d.URL, engineHealthPath(d), strings.Join(st.modelIDs(ctx), ","))
			continue
		}
		st := &engineRuntimeState{
			def: d,
			ecs: &engineECS{
				api: ecsc, key: d.Key, cluster: cluster,
				service:  d.Service,
				roleAttr: engineBoxRole(d),
			},
			apiKey:      readEngineAPIKey(ctx, ssmc, d),
			settings:    settings,
			catalog:     newEngineCatalog(models, d.Key),
			ssm:         ssmc,
			activeParam: engineActiveParamName(name, d.Key),
			pending:     newEnginePending(ssmc, name, d.Key),
			classes:     parseEngineOffers(d.Key, d.offersSpec()),
			cluster:     cluster,
			offers:      newEngineOfferRun(d.offerBudget()),
			audit:       auditor,
		}
		// Published at start as well as on every change: the box reads it when it starts, and a
		// CP that came up after a catalogue edit it never saw (another replica's, or one made
		// while this process was down) would otherwise leave the parameter stale forever.
		if err := st.publishActiveSet(ctx); err != nil {
			log.Printf("engines: %v", err)
		}
		cfg := engineControlCfgFor(d)
		st.demand = newEngineDemand(settings, engineSettingsFor(d.Key).demandAt, cfg.window)
		st.ctrl = newEngineController(st.ecs, engineSettingsFor(d.Key), st.warmProbe, st.demand, settings, auditor, cfg)
		// The controller must not buy a $1.26/hour box for an engine that has nothing to load.
		// Without this, `mode=on` with an empty catalogue starts the placeholder container
		// (`sleep infinity`) and the controller then watches it for ever at 5-second intervals,
		// because `running && !warmed` is not a failure state (ADR 0072 decision 1(c)).
		st.ctrl.hasModels = st.catalog.hasModels
		// The purchasing side (ADR 0077). newEngineFleet answers nil for a row that declares no
		// launch template, and wireOffers attaches nothing for a nil one — so a deployment that
		// does not buy boxes has no path to EC2 at all.
		st.wireOffers(newEngineFleet(ec2c, d.Key, cluster, d.LaunchTemplate, engineSubnets()))
		// The controller doubles as the uptime sampler (engine_uptime.go). Attached here and
		// not inside newEngineController because the VOICEVOX controller shares that
		// constructor and has no heatmap to feed: an INSERT every 30 seconds for a series
		// nothing reads is a cost with no reader.
		if mgr != nil && mgr.store != nil {
			st.ctrl.uptime = mgr.store
		}
		reg.byKey[d.Key] = st
		log.Printf("engines: %s (%s) -> %s (service=%s idle=%s deadline=%s models=%s)",
			d.Key, d.api(), d.URL, d.Service, cfg.idle, cfg.deadline,
			strings.Join(st.modelIDs(ctx), ","))
		if cfg.interval > 0 {
			go st.ctrl.run(context.Background())
		}
	}
	// The table itself is re-read from here on (ADR 0074, the gap PR #520 measured): a rung
	// changed by a CloudFormation update used to reach a running CP only through a blue/green
	// of the CP. Seeded with an EMPTY value rather than the text this process started from —
	// the first tick then parses the table once and finds nothing to change, which costs one
	// parse and saves threading the raw parameter out of loadEngineTable.
	if haveAWS {
		go newEngineTableReloader(ssmc, name, reg, "").run(context.Background())
	}
	return reg
}

// readEngineAPIKey fetches the engine's own --api-key. Not fatal when it fails: an engine
// started without one still answers, and refusing to serve because the SECOND lock could not
// be read would turn a defence-in-depth measure into a single point of failure. The
// reachability rule (the SG lets only the CP in) is the first lock and does not depend on
// this.
func readEngineAPIKey(ctx context.Context, api engineSSMAPI, d engineDef) string {
	if strings.TrimSpace(d.APIKeyParam) == "" || api == nil {
		return ""
	}
	out, err := api.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(d.APIKeyParam), WithDecryption: aws.Bool(true),
	})
	if err != nil {
		log.Printf("engines: %s api key (%s) unreadable: %v", d.Key, d.APIKeyParam, err)
		return ""
	}
	return strings.TrimSpace(aws.ToString(out.Parameter.Value))
}

// mode is the engine's current mode, the stored setting winning over the stack's default.
//
// Whether anything here can start and stop the engine decides two of the answers, which is why
// it is asked rather than assumed (ADR 0076 decision 5): an externally managed engine defaults
// to `on` instead of `ondemand`, and a stored `ondemand` reads as `on` — there is no box to
// stop, and the value is reachable on such a row through a stack default or a client written
// before this distinction existed.
func (e *engineRuntimeState) mode(ctx context.Context) string {
	managed := e.ecs != nil
	if e.settings != nil {
		if v, _ := e.settings.GetSetting(ctx, engineSettingsFor(e.def.Key).mode); strings.TrimSpace(v) != "" {
			return engineMode(v, managed)
		}
	}
	return engineMode(e.def.Mode, managed)
}

// engineExternalWarmTTL and engineExternalWarmTimeout bound the ONE health call an externally
// managed engine's `warm` costs (ADR 0076 decision 8).
//
// 2 seconds rather than the gateway's 5: the admin list handler is synchronous and the Console
// asks on every load, so a LAN box that is switched off would otherwise hold the whole panel
// for 5 seconds per external row. The 10-second cache is what keeps a panel that polls from
// turning into a dial loop.
const (
	engineExternalWarmTTL     = 10 * time.Second
	engineExternalWarmTimeout = 2 * time.Second
)

// engineWarmCache is the health answer an external engine's panel row is built from.
type engineWarmCache struct {
	mu   sync.Mutex
	at   time.Time
	warm bool
}

// warm is "does this engine have something loaded", answered the only way each kind of engine
// can answer it: a managed one has a controller keeping the flag on its own tick, and an
// external one has nothing but the health endpoint. False for an engine that has neither,
// which is the honest answer — no observation was made.
//
// The lock is held ACROSS the call on purpose: two panels loading at once then cost one probe
// rather than two, and the 2-second timeout is what makes that safe to wait behind.
// negativeAlways is what this engine's administrator excludes from every image (ADR 0072
// follow-up, negative prompts). "" when nothing was configured, when the settings store is
// absent (tests), and for an engine whose settings have no such row.
//
// Read on the catalogue path rather than held in memory: it changes from a text box in the admin
// panel, and the Agent's own catalogue cache is what bounds how often it is asked for (10
// minutes) — the same TTL that already bounds a model being enabled.
func (e *engineRuntimeState) negativeAlways(ctx context.Context) string {
	keys := engineSettingsFor(e.def.Key)
	if e.settings == nil || keys.negative == "" {
		return ""
	}
	v, _ := e.settings.GetSetting(ctx, keys.negative)
	return strings.TrimSpace(v)
}

func (e *engineRuntimeState) warm(ctx context.Context) bool {
	if e.ctrl != nil {
		return e.ctrl.warmed()
	}
	if !e.def.external() {
		return false
	}
	e.extWarm.mu.Lock()
	defer e.extWarm.mu.Unlock()
	if !e.extWarm.at.IsZero() && time.Since(e.extWarm.at) < engineExternalWarmTTL {
		return e.extWarm.warm
	}
	c, cancel := context.WithTimeout(ctx, engineExternalWarmTimeout)
	defer cancel()
	e.extWarm.warm, e.extWarm.at = engineHealthy(c, e), time.Now()
	return e.extWarm.warm
}

// controlCfg is the tuning that governs this engine, whether or not a controller is running.
// Falling back to the stack's declaration rather than to a zero value matters: the admin panel
// reads the idle window out of this to say when the engine will stop by itself, and a zero
// there is configured to mean "never stops", which is the opposite of the truth for a managed
// engine that simply has no loop attached in this process.
func (e *engineRuntimeState) controlCfg() engineControlCfg {
	if e.ctrl != nil {
		return e.ctrl.cfg
	}
	return engineControlCfgFor(e.def)
}

// modelIDs are the ids this engine's provider offers, as <provider>/<id>. Read from the
// CATALOGUE, not from the stack (ADR 0072 decision 1): a model an administrator switched off
// is not something a session should find in its launch menu, and the stack no longer knows
// which those are.
func (e *engineRuntimeState) modelIDs(ctx context.Context) []string {
	provider := e.def.Provider
	if provider == "" {
		provider = e.def.Key
	}
	rows := e.catalog.enabled(ctx)
	out := make([]string, 0, len(rows))
	for _, m := range rows {
		if engineModelIsLora(m) {
			continue
		}
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, provider+"/"+id)
		}
	}
	return out
}
