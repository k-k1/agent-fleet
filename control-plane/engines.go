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
	Key              string `json:"key"`              // "llm" — the path segment, the log prefix, the settings prefix
	API              string `json:"api"`              // "chat" | "images" — see engineAPI* below
	Service          string `json:"service"`          // ECS service whose desired count moves
	CapacityProvider string `json:"capacityProvider"` // what makes `draining` observable; empty = Fargate
	URL              string `json:"url"`              // http://llm.af.internal:8080
	Health           string `json:"health"`           // "/health"
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
	// means the box is whatever CloudFormation put in the capacity provider and nothing here
	// ever calls ECS about it.
	//
	// It sits with the vessel rather than with the catalogue because a rung IS the vessel: the
	// stack owns the capacity provider, and this only says which of the shapes the operator
	// blessed may be selected. WHICH one is selected is a stored setting (decision 2).
	Classes          string `json:"classes"`
	IdleSec          int    `json:"idleSec"`
	StartDeadlineSec int    `json:"startDeadlineSec"`
	Mode             string `json:"mode"` // the DEFAULT mode; a stored setting wins
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
	served      engineServed
	// classes is the GPU ladder (ADR 0074). Empty on every deployment that declares none, and
	// then nothing in engine_class.go ever runs. capacity and cluster are how a rung reaches
	// the capacity provider; capacity is nil on a CP with no AWS.
	classes  []engineClass
	capacity engineCapacityAPI
	cluster  string
	// appliedClass is the rung THIS PROCESS last wrote to the capacity provider, and nothing
	// else. It is in memory on purpose: what the provider currently holds is a fact about AWS,
	// and a CP that restarted has not observed it — so a restarted CP re-applies once before
	// the next start rather than trusting a note it wrote before.
	appliedMu    sync.Mutex
	appliedClass string
	// classApplyErr is why the last write to the capacity provider failed, "" when the last one
	// succeeded. In memory for the same reason appliedClass is, and the absence of one is never
	// read as "it worked": a restarted CP has applied nothing and must not claim a failure it
	// did not see. What makes that gap safe is that a start applies the rung again anyway.
	classApplyErr string
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

// engineSettingsFor names an engine's three settings rows. VOICEVOX keeps the names ADR
// 0070 shipped (ttsEngineSettings); everything since is prefixed by its key.
func engineSettingsFor(key string) engineSettings {
	return engineSettings{
		mode:     "engine_" + key + "_mode",
		modeAt:   "engine_" + key + "_mode_at",
		demandAt: "engine_" + key + "_demand_at",
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
		if d.Key == "" || d.Service == "" || d.URL == "" {
			return engineTable{}, fmt.Errorf("engine %d: key, service and url are all required", i)
		}
	}
	return t, nil
}

// newEngineRegistry builds and starts the engines. A failure to read the table is logged
// and leaves the registry empty rather than stopping the CP: the deployment's Workspaces,
// sessions and everything else do not depend on an engine existing, and refusing to boot
// over an optional feature is the larger outage.
func newEngineRegistry(ctx context.Context, mgr *manager) *engineRegistry {
	name := strings.TrimSpace(envx.Or("AF_ENGINES_SSM_PARAM", ""))
	inline := strings.TrimSpace(envx.Or("AF_ENGINES_JSON", ""))
	if name == "" && inline == "" {
		return nil
	}
	region := firstEnv("AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
	ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if err != nil {
		log.Printf("engines: disabled (aws config: %v)", err)
		return nil
	}
	ssmc := ssm.NewFromConfig(ac)
	table, err := loadEngineTable(ctx, ssmc)
	if err != nil {
		log.Printf("engines: disabled (%v)", err)
		return nil
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
	ecsc := ecs.NewFromConfig(ac)
	cluster := firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")
	// Taking models in (ADR 0072 decision 6). Only wired when the stack declared an ingest task
	// AND there is somewhere to keep the jobs: without either, the panel says so and the manual
	// route stays. The reconcile loop is started here rather than per engine — one task
	// definition serves both roles.
	if mgr != nil && mgr.store != nil && table.Ingest.ok() {
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
		st := &engineRuntimeState{
			def: d,
			ecs: &engineECS{
				api: ecsc, key: d.Key, cluster: cluster,
				service: d.Service, capacityProvider: d.CapacityProvider,
			},
			apiKey:      readEngineAPIKey(ctx, ssmc, d),
			settings:    settings,
			catalog:     newEngineCatalog(models, d.Key),
			ssm:         ssmc,
			activeParam: engineActiveParamName(name, d.Key),
			classes:     parseEngineClasses(d.Classes),
			cluster:     cluster,
		}
		// The ECS client is attached only when there is a ladder to apply. Not an optimisation:
		// it is what makes ADR 0074 decision 3 checkable — with no rung declared there is no
		// path from here to DescribeCapacityProviders at all, so a deployment that never
		// configures this cannot log an AccessDenied for it.
		if len(st.classes) > 0 {
			st.capacity = ecsc
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
		// Attached only when a ladder exists, so an engine without one keeps exactly the start
		// path it had before this ADR (ADR 0074 decision 3).
		if len(st.classes) > 0 {
			st.ctrl.startGate = st.startGate
		}
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
func (e *engineRuntimeState) mode(ctx context.Context) string {
	if e.settings != nil {
		if v, _ := e.settings.GetSetting(ctx, engineSettingsFor(e.def.Key).mode); strings.TrimSpace(v) != "" {
			return engineMode(v, true)
		}
	}
	return engineMode(e.def.Mode, true)
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
