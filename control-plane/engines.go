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
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineDef is one row of the table 60-engines wrote. Every field is declared by the stack
// rather than derived here (ADR 0053): the engine is asleep most of the time, so anything
// the CP would have to ask the engine for is something it cannot ask.
//
// ContextTokens and MaxOutputTokens sit on the ENGINE rather than on each model, because that
// is the real granularity: one llama-server process serves ONE gguf with ONE -c, and the
// several ids in Models are aliases pointing at that one window. Two models with different
// windows are two engines — two rows, and two provider ids, since the Agent keys opencode's
// provider block by Provider and the second would otherwise overwrite the first. Zero means a
// stack older than the field, and the Agent then writes no limit at all rather than guessing.
type engineDef struct {
	Key              string   `json:"key"`              // "llm" — the path segment, the log prefix, the settings prefix
	API              string   `json:"api"`              // "chat" | "images" — see engineAPI* below
	Service          string   `json:"service"`          // ECS service whose desired count moves
	CapacityProvider string   `json:"capacityProvider"` // what makes `draining` observable; empty = Fargate
	URL              string   `json:"url"`              // http://llm.af.internal:8080
	Health           string   `json:"health"`           // "/health"
	Provider         string   `json:"provider"`         // "llamacpp" — the provider id a Workspace configures
	Models           []string `json:"models"`           // model ids offered as <provider>/<id>
	ContextTokens    int      `json:"contextTokens"`    // the window the engine is STARTED with (llama-server -c)
	MaxOutputTokens  int      `json:"maxOutputTokens"`  // output cap advertised with it
	APIKeyParam      string   `json:"apiKeyParam"`      // SSM SecureString the engine's own --api-key is in
	IdleSec          int      `json:"idleSec"`
	StartDeadlineSec int      `json:"startDeadlineSec"`
	Mode             string   `json:"mode"` // the DEFAULT mode; a stored setting wins
}

type engineTable struct {
	Engines []engineDef `json:"engines"`
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
}

// engineRegistry is every engine this deployment runs. Nil (or empty) is the normal case —
// self-hosted inference is opt-in and a GPU box is $1.26/hour — and every entry point
// checks for it rather than assuming.
type engineRegistry struct {
	mu      sync.RWMutex
	byKey   map[string]*engineRuntimeState
	signKey []byte
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
	if mgr != nil && mgr.store != nil {
		settings = mgr.store
		auditor = mgr.store
	}
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}}
	if mgr != nil {
		reg.signKey = engineSignKey(mgr.tokenSignMaster())
	}
	ecsc := ecs.NewFromConfig(ac)
	cluster := firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")
	for _, d := range table.Engines {
		st := &engineRuntimeState{
			def: d,
			ecs: &engineECS{
				api: ecsc, key: d.Key, cluster: cluster,
				service: d.Service, capacityProvider: d.CapacityProvider,
			},
			apiKey:   readEngineAPIKey(ctx, ssmc, d),
			settings: settings,
		}
		cfg := engineControlCfgFor(d)
		st.demand = newEngineDemand(settings, engineSettingsFor(d.Key).demandAt, cfg.window)
		st.ctrl = newEngineController(st.ecs, engineSettingsFor(d.Key), st.warmProbe, st.demand, settings, auditor, cfg)
		// The controller doubles as the uptime sampler (engine_uptime.go). Attached here and
		// not inside newEngineController because the VOICEVOX controller shares that
		// constructor and has no heatmap to feed: an INSERT every 30 seconds for a series
		// nothing reads is a cost with no reader.
		if mgr != nil && mgr.store != nil {
			st.ctrl.uptime = mgr.store
		}
		reg.byKey[d.Key] = st
		log.Printf("engines: %s (%s) -> %s (service=%s idle=%s deadline=%s models=%s)",
			d.Key, d.api(), d.URL, d.Service, cfg.idle, cfg.deadline, strings.Join(d.Models, ","))
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

// modelIDs are the ids this engine's provider offers, as <provider>/<id>.
func (e *engineRuntimeState) modelIDs() []string {
	provider := e.def.Provider
	if provider == "" {
		provider = e.def.Key
	}
	out := make([]string, 0, len(e.def.Models))
	for _, m := range e.def.Models {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, provider+"/"+m)
		}
	}
	return out
}
