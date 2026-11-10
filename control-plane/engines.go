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
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

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
	// jobs records which membership submitted each ComfyUI prompt (engine_comfy_guard.go); read
	// through jobOwners().
	jobsOnce sync.Once
	jobs     *engineJobOwners
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
	// extDown is the same idea for the GENERATION path: whether this external row was found not
	// answering, so the next request refuses at once instead of paying the probe again (ADR 0082
	// unresolved 1). Separate from extWarm because the two probes have different budgets — the
	// panel's is 2 seconds, the gateway's is 5 — and a panel load must not settle what a
	// generation is told, nor the other way round. Inert for every engine that has a controller.
	extDown engineDownCache
	// discover is the cached answer to the last press of the discovery button (ADR 0082
	// decisions 6 and 7, engine_discover.go). Inert for every row that is not an external comfy
	// one — nothing else ever populates it.
	discover engineDiscoverCache
	// remote is the far deployment this row borrows from (ADR 0079). Non-nil ONLY for a row whose
	// lifecycle is `remote`; every method on it is nil-safe, so the branches that reach it do not
	// each need a guard. It owns the three things that differ from an external row: the token
	// exchange, the mirrored catalogue, and the warmth the far side reports.
	remote *engineRemote
	// swapWaitSince is when this process first refused to start because a box of the previous
	// rung was still registered. It bounds that wait (engineClassSwapWaitMax): the end of the
	// wait belongs to AWS, and `scaleInAfter: -1` would otherwise make it never end.
	swapWaitSince time.Time
	// inflight is the CP gateway's own count of requests it currently holds open for this row —
	// ADR 0084 decision 6-A's "A". Every row gets one, managed or not: the gateway proxies an
	// external or remote row's requests too (decision 8's gate 3 applies to all three
	// lifecycles), so all three can be waited on. Methods are nil-safe, like engineDemand's,
	// for the test helpers that build a row by hand without wiring one.
	inflight *engineInFlight
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
	// build constructs one row's runtime with everything a boot-time engine gets: the clients,
	// the stores, the cluster, the controller and its goroutine. It is held here so that the
	// table reloader can adopt a role this process does not serve yet — see adopt().
	//
	// nil on a CP with no AWS (an inline AF_ENGINES_JSON table, a dev process): such a table
	// changes only when the process is restarted, so there is nothing to adopt.
	build func(engineDef) *engineRuntimeState
	// startIngest attaches the deployment-wide ingest runner, for the same window: the table
	// that came back with no rows carried no `ingest` block either, and without this a CP that
	// booted inside the migration could serve every engine and still not be able to take a model
	// in until it was replaced. nil for the same deployments build is nil for.
	startIngest func(engineIngestDef) bool
	// ingest is deployment-wide (one task definition serves both roles), so it hangs off the
	// registry rather than off an engine. Nil when the stack declares none — a deployment
	// running an ADR 0071 table, or one whose CP has no AWS at all.
	ing *engineIngester
	// buildComfy is build, always set, for the image row the admin panel replaces at run time
	// (engine_comfy_panel.go). Separate from build because build's nil-ness is what tells adopt
	// there is nothing to adopt with.
	buildComfy func(engineDef) *engineRuntimeState
	// comfyFallback is the external image row this process would serve without the panel —
	// AF_COMFY_URL's, or an external row of the table — which clearing the panel returns to. nil
	// when there is none, and then clearing the panel takes the role away.
	comfyFallback *engineDef
	// comfyMu serialises a panel change from reading the current row to swapping it, and
	// comfyWriteMu one save from the stored value to the swap (engine_comfy_panel.go).
	comfyMu      sync.Mutex
	comfyWriteMu sync.Mutex
}

// ingester is the ingest runner, or nil when this deployment has none.
//
// Under the lock because it can now be attached AFTER boot, by the table reloader's goroutine
// (see startIngest), while a request is reading it.
func (r *engineRegistry) ingester() *engineIngester {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ing
}

// ingestDef is what the stack declared about taking models in. The zero value is a complete
// answer: `hasToken` false and `ok()` false mean "no gated repositories, no ingest".
func (r *engineRegistry) ingestDef() engineIngestDef {
	return r.ingester().ingestDef()
}

// adopt registers a role the table now declares and this process does not serve.
//
// 🔴 It exists because of a window the ADR 0077 migration opens and hardware walked into: the
// `<Role>Enabled` round trip drops the whole 60-engines stack's conditional resources for a
// minute or two, the engine TABLE among them, and a Control Plane that starts inside that window
// reads a table with NO ROWS. Before this it answered `{"engines":[]}` for the rest of its life —
// no reloader was even created — and the only way back was a force-new-deployment of the CP
// (measured: 86 s, ADR 0077 P1 hardware run). A row appearing is now the ordinary case it always
// was for a rung or a launch template.
//
// It reports whether the role was taken on. The construction is the SAME function boot uses, so
// an adopted engine has the state the rest of this process assumes — which is what
// engine_table_reload.go's header says the alternative could not have.
func (r *engineRegistry) adopt(d engineDef) bool {
	if r == nil || r.build == nil {
		return false
	}
	st := r.build(d)
	if st == nil {
		return false
	}
	r.mu.Lock()
	if r.byKey[d.Key] != nil {
		// Somebody else got there first (two ticks, or a boot that raced the poll). Whatever was
		// built here has no controller running yet and is simply dropped.
		r.mu.Unlock()
		return false
	}
	r.byKey[d.Key] = st
	r.mu.Unlock()
	// Registered FIRST, then driven: the controller reads the registry through the engine it was
	// given, and a loop that started before the role was visible would be a box nothing on the
	// admin side can see.
	if st.ctrl != nil && st.ctrl.cfg.interval > 0 {
		go st.ctrl.run(context.Background())
	}
	return true
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
		idle:     "engine_" + key + "_idle_sec",
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

// newEngineRegistry builds and starts the engines. A failure to read the table is logged
// and leaves the registry empty rather than stopping the CP: the deployment's Workspaces,
// sessions and everything else do not depend on an engine existing, and refusing to boot
// over an optional feature is the larger outage.
//
// ctx also ends the borrowed-catalogue poll; the controllers and the table reloader outlive it.
func newEngineRegistry(ctx context.Context, mgr *manager) *engineRegistry {
	name := strings.TrimSpace(envx.Or("AF_ENGINES_SSM_PARAM", ""))
	inline := strings.TrimSpace(envx.Or("AF_ENGINES_JSON", ""))
	envRow, envAPIKey, hasEnvRow := engineComfyEnvRow()
	llmEnvRow, hasLlmEnvRow := engineLlmEnvRow()
	// Borrowing is the fourth way a deployment can have engines (ADR 0079 decision 2), and it has
	// to be on this gate: a CP that declares nothing but AF_REMOTE_ENGINE_URL used to return here
	// with no registry at all — and registerEngineRoutes then skips the gateway routes entirely,
	// so `/engine/…` would not even be routed.
	rem := newEngineRemotes(mgr)
	// A CP with a store can be given a LAN ComfyUI from the admin panel at any time (#957), so it
	// always gets a registry — possibly an empty one — for the panel to put the row into. Without
	// it a first save on a deployment that booted with no engines would still need a restart.
	panel := newEngineComfyPanel(mgr)
	if name == "" && inline == "" && !hasEnvRow && !hasLlmEnvRow && rem == nil && panel == nil {
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
		table.Engines = engineTableWithEnvRow(table.Engines, envRow, "AF_COMFY_URL")
	}
	if hasLlmEnvRow {
		table.Engines = engineTableWithEnvRow(table.Engines, llmEnvRow, "AF_LLM_URL")
	}
	// The panel's URL goes on top of the environment's, after the row it would replace has been
	// kept as the fallback that clearing the panel returns to. The same merge as the environment's,
	// so a managed table row wins over the panel too (ADR 0076 decision 2, 2026-10-04 addendum).
	var comfyFallback *engineDef
	for _, d := range table.Engines {
		if d.Key == "image" && d.notManagedHere() {
			d := d
			comfyFallback = &d
		}
	}
	panelURL, panelKey := panel.boot(ctx)
	panelRow := engineComfyRow(panelURL, engineOriginPanel)
	if panelURL != "" {
		table.Engines = engineTableWithEnvRow(table.Engines, panelRow, "the admin panel")
	}
	// 🔴 NOT "return nil" any more (ADR 0077 P1 hardware run). A table with no rows is a real
	// state of a deployment that is mid-migration — the `<Role>Enabled` round trip drops the
	// conditional resources, the engine table among them — and a CP that started in that window
	// used to answer `{"engines":[]}` for the rest of its life, with no reloader to notice the
	// rows coming back. Measured: 86 seconds of force-new-deployment to recover. The registry is
	// now built empty and the poll below adopts what appears.
	//
	// A deployment that declares no table at all still gets nothing: `name` and `inline` are both
	// empty and this function returned nil long before here.
	//
	// 🔴 `rem != nil` belongs here for the same reason `haveAWS` does, and it is the gate ADR 0079's
	// review found: a borrowing native or docker CP has NO rows at boot — they arrive with the first
	// catalogue fetch (decision 2) — and no AWS either, so it used to fall out here and serve
	// nothing for the rest of its life.
	if len(table.Engines) == 0 && !haveAWS && rem == nil && panel == nil {
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
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}, comfyFallback: comfyFallback}
	if mgr != nil {
		reg.signKey = engineSignKey(mgr.tokenSigningRoot())
	}
	cluster := firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")
	// Taking models in (ADR 0072 decision 6). Only wired when the stack declared an ingest task
	// AND there is somewhere to keep the jobs: without either, the panel says so and the manual
	// route stays. The reconcile loop is started here rather than per engine — one task
	// definition serves both roles.
	if haveAWS && mgr != nil && mgr.store != nil {
		reg.startIngest = func(def engineIngestDef) bool {
			if !def.ok() {
				return false
			}
			secrets := func(d engineIngestDef) (*engineHfTokens, *engineCivitaiTokens) {
				return newEngineHfTokens(d, mgr.store, mgr, secretsmanager.NewFromConfig(ac)),
					newEngineCivitaiTokens(d, mgr.store, mgr, secretsmanager.NewFromConfig(ac))
			}
			// 🔴 Already running: the RUNNER stays — its reconcile loop owns the jobs it started —
			// but everything the table decides is taken again. A CloudFormation update registers a
			// new ingest task definition revision and deregisters the previous one, so an ingester
			// that kept the block it booted with fails every ingest with `TaskDefinition is
			// inactive` until the Control Plane is replaced (af-sandbox, 2026-09-15).
			if ing := reg.ingester(); ing != nil {
				moved := ing.adopt(def, secrets)
				if ing.setStorage(newEngineStorage(def.Bucket,
					newEngineAWSStorageMetadata(def.Bucket, s3.NewFromConfig(ac)))) {
					moved = true
				}
				return moved
			}
			ing := &engineIngester{
				def: def, cluster: cluster, ecs: ecsc,
				logs:   newEngineIngestLogs(ac),
				store:  mgr.store,
				models: mgr.store,
				storage: newEngineStorage(def.Bucket,
					newEngineAWSStorageMetadata(def.Bucket, s3.NewFromConfig(ac))),
				tokens:        newEngineHfTokens(def, mgr.store, mgr, secretsmanager.NewFromConfig(ac)),
				civitaiTokens: newEngineCivitaiTokens(def, mgr.store, mgr, secretsmanager.NewFromConfig(ac)),
				onDone: func(role string) {
					if e := reg.get(role); e != nil {
						e.catalog.invalidate()
						if err := e.publishActiveSet(context.Background()); err != nil {
							log.Printf("engines: %v", err)
						}
					}
					// …and the same push putModel and deleteModel make. An ingest that CREATES a
					// row changes nothing a workspace can see — the row arrives disabled — but one
					// that replaces a file under an enabled row changes what the box loads, and a
					// session holding a catalogue from before the swap would go on naming the file
					// that is gone until its own 10-minute TTL.
					go notifyEngineCatalogChanged(context.Background(), mgr, role)
				},
			}
			reg.mu.Lock()
			reg.ing = ing
			reg.mu.Unlock()
			go ing.run(context.Background())
			return true
		}
		reg.startIngest(table.Ingest)
	}
	// One row's runtime, built the same way whether this process found it in the table at boot
	// or the reloader met it later (adopt). Everything it needs is captured here rather than
	// passed: the clients, the stores, the cluster and the parameter name.
	build := func(d engineDef) *engineRuntimeState {
		// Detached from the caller's context on purpose. At boot this is the server's startup
		// context; an adopted row is built from a poll minutes later, and an engine that failed
		// to publish its active set because a startup context had ended would be a box that
		// loads nothing.
		ctx := context.WithoutCancel(ctx)
		// Said once per (re)build — at boot, and again if the table reloader or the remote
		// catalogue mirror adopts this row later — rather than gated into silence: this row still
		// gets a runtime state below, so it shows up in the admin panel with the mark row() adds,
		// instead of just not existing where an operator would look for it (ADR 0083 decision 5).
		if !imageProviderServable(d) {
			log.Printf("engines: %s declares images provider %q, which this build does not implement a client for (servable: comfy, openai-compat) - generate_image on this row will not work",
				d.Key, d.Provider)
		}
		// 🔴 A remote row with nothing to borrow FROM is refused rather than served. The shape that
		// reaches here is a hand-written `lifecycle:"remote"` table row on a deployment where
		// AF_REMOTE_ENGINE_URL / _TOKEN are unset: without the handle there is no catalogue source,
		// and a catalogue that cannot be read at all answers hasModels TRUE (engine_catalog.go) — so
		// the row would pass serve's no-models gate and then 404 `model_unknown` on every request
		// that names a model. That is precisely the failure ADR 0079 decision 7 exists to prevent,
		// and it is quieter than the row simply not being there.
		if d.remote() && rem == nil {
			log.Printf("engines: %s declares lifecycle %q but AF_REMOTE_ENGINE_URL / AF_REMOTE_ENGINE_TOKEN are unset, so there is nothing to borrow from - the role is not served",
				d.Key, engineLifecycleRemote)
			return nil
		}
		if d.notManagedHere() {
			// Everything a row this deployment does not own does NOT get (ADR 0076 decision 1,
			// ADR 0079 decision 1): no ECS adapter, no controller, no demand counter, no active
			// set, no pending reader, no GPU ladder and no uptime sampler. The catalogue and the
			// settings store stay, because what may be generated and whether the route is open are
			// still this deployment's answers.
			st := &engineRuntimeState{
				def:      d,
				settings: settings,
				catalog:  newEngineCatalog(models, d.Key),
				inflight: newEngineInFlight(),
			}
			// The bearer the reverse proxy in front of ComfyUI checks (ADR 0076 decision 7), and —
			// for every OTHER external row — the one AF_ENGINE_API_KEY_<KEY> declares (ADR 0079
			// decision 11). Same field a managed row fills from SSM, so dial and engineHealthy
			// already present it; SSM is never read for either of these lifecycles, which is the
			// point of the lane.
			//
			// AF_COMFY_API_KEY wins on the row AF_COMFY_URL synthesised: it is the more specific of
			// the two, and it is what every ADR 0076 deployment already sets. One line when both are
			// declared, because a bearer that is silently not the one the operator just edited is a
			// 401 with nothing to read.
			//
			// A row the admin panel synthesised gets neither: its key is the panel's own, set by the
			// caller (the boot loop below, applyComfyPanel), and engineEnvAPIKey refuses it.
			if hasEnvRow && d == envRow && envAPIKey != "" {
				st.apiKey = envAPIKey
				if other := engineEnvAPIKey(d); other != "" && other != envAPIKey {
					log.Printf("engines: %s has a bearer in both AF_COMFY_API_KEY and %s, and AF_COMFY_API_KEY wins",
						d.Key, engineAPIKeyEnvName(d.Key))
				}
			} else if k := engineEnvAPIKey(d); k != "" {
				st.apiKey = k
				log.Printf("engines: %s presents the bearer from %s upstream", d.Key, engineAPIKeyEnvName(d.Key))
			}
			// A remote row's catalogue is the FAR deployment's, mirrored read-only, and its
			// credential is minted per (key, session) rather than held in apiKey (ADR 0079
			// decisions 7 and 4). Both hang off this one handle so that nothing else in the
			// process has to know which flavour of "somebody else's" a row is.
			if d.remote() {
				st.remote = rem.forKey(d.Key)
				st.catalog.source = st.remote.catalogSource()
			}
			// No `service=`: there is none, and printing an empty one reads as a truncated line.
			log.Printf("engines: %s (%s) -> %s (%s, health=%s models=%s)",
				d.Key, d.api(), d.URL, d.lifecycle(), engineHealthPath(d), strings.Join(st.modelIDs(ctx), ","))
			return st
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
			classes:     parseEngineClasses(d.offersSpec()),
			cluster:     cluster,
			offers:      newEngineOfferRun(d.offerBudget()),
			audit:       auditor,
			inflight:    newEngineInFlight(),
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
		log.Printf("engines: %s (%s) -> %s (service=%s idle=%s deadline=%s models=%s)",
			d.Key, d.api(), d.URL, d.Service, cfg.idle, cfg.deadline,
			strings.Join(st.modelIDs(ctx), ","))
		return st
	}
	// Whoever adopts a role later needs this, and there are two such pollers now: the table
	// reloader (ADR 0077 P1, AWS only) and the remote catalogue mirror (ADR 0079 decision 2). An
	// inline AF_ENGINES_JSON table with neither cannot change under a running process, so a
	// builder there would be a closure nobody calls.
	if haveAWS || rem != nil {
		reg.build = build
	}
	// The panel rebuilds the image row whenever an administrator saves, on every deployment —
	// including one with no AWS and nothing to borrow, where build above stays unset.
	reg.buildComfy = build
	for _, d := range table.Engines {
		st := build(d)
		if st == nil {
			continue
		}
		if d.origin == engineOriginPanel {
			st.apiKey = panelKey
		}
		reg.byKey[d.Key] = st
		if st.ctrl != nil && st.ctrl.cfg.interval > 0 {
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
	// The borrowed rows arrive the same way a rung does, on their own poll: the far catalogue is
	// what declares which engines exist, so there is nothing to read at boot (ADR 0079 decision 2).
	// It also refreshes the mirror every row already reads through.
	rem.run(ctx, reg)
	return reg
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

// engineExternalDownTTL is how long the generation path trusts "that box is not answering"
// before paying for the probe again (ADR 0082 unresolved 1). Deliberately the same window as
// engineExternalWarmTTL: both answer "how stale may an observation of an external row be", and
// two numbers would be two knobs for one question.
//
// Measured 2026-09-14, against an operator's LAN host that was switched off, from a container on
// the same network: a connect attempt ended in `No route to host` after 3.05-3.11 s (four
// samples). The two shapes the ADR expected bracket it — a refused connection came back in
// 0.15 ms, and an address that drops the SYN silently held for the full 5.00 s cap — so a
// switched-off machine on a LAN costs 3 seconds, not the 5 the cap suggests, because the kernel
// gives up on ARP first. Without this cache ensureReady pays that on EVERY picture, in front of
// the preferred route: the fallback to the next provider is supposed to be the cheap part.
const engineExternalDownTTL = engineExternalWarmTTL

// engineDownCache is the generation path's memory of an external row that was not answering.
// Only a NEGATIVE is cached: a box that has come up must be usable on the next request, so a
// healthy probe clears it rather than being remembered for the window.
type engineDownCache struct {
	mu   sync.Mutex
	at   time.Time
	down bool
}

// downRecently reports whether this row was found unreachable inside the window, which is the
// one case where skipping the probe changes nothing: nothing here can start an external row
// (ADR 0076 decision 4), so the probe's only outcome would be the same refusal.
func (e *engineRuntimeState) downRecently() bool {
	e.extDown.mu.Lock()
	defer e.extDown.mu.Unlock()
	return e.extDown.down && !e.extDown.at.IsZero() && time.Since(e.extDown.at) < engineExternalDownTTL
}

// noteProbe records what the generation path's health call found. Called for external rows only:
// a managed row that answers nothing is being STARTED, and remembering "down" there would make
// ensureReady refuse the box it is waiting for.
func (e *engineRuntimeState) noteProbe(healthy bool) {
	e.extDown.mu.Lock()
	defer e.extDown.mu.Unlock()
	e.extDown.down, e.extDown.at = !healthy, time.Now()
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
	// A borrowed engine's exclusion list is the FAR administrator's, and it rides on the catalogue
	// this row mirrors (ADR 0079 decision 7). Reading the local setting instead would answer with
	// something nobody on the deployment that owns the engine can see — and writing one is refused
	// for the same reason.
	if e.def.remote() {
		return e.remote.negativeAlways()
	}
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
	// 🔴 A REMOTE row is neither of the other two answers, and this is the site ADR 0079's review
	// found broken: probing it would be the health call decision 5 refuses — from the admin panel,
	// on every load — and answering false would report every borrowed engine cold for ever. The
	// far deployment already publishes its own observation in the catalogue this row mirrors
	// (ADR 0079 decision 10), so warmth is READ rather than asked for.
	if e.def.remote() {
		return e.remote.warm()
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
func (e *engineRuntimeState) controlCfg(ctx context.Context) engineControlCfg {
	var cfg engineControlCfg
	if e.ctrl != nil {
		cfg = e.ctrl.cfg
	} else {
		cfg = engineControlCfgFor(e.def)
	}
	return engineControlCfgWithStoredIdle(ctx, cfg, e.settings, engineSettingsFor(e.def.Key).idle)
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

// modelLabel is the member-facing name (ADR 0090 decision 2) for one of this engine's ids.
//
// "" for an id the catalogue does not hold — a model disabled after it was loaded, or a name the
// router answered with that was never a catalogue row — and for a row nobody has read a model
// page for. Every reader falls back to the id in both cases, which is what ADR 0090 asks of an
// absent label and is the behaviour that existed before the field did.
func (e *engineRuntimeState) modelLabel(ctx context.Context, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, m := range e.catalog.enabled(ctx) {
		if m.ID == id {
			return engineModelLabel(m)
		}
	}
	return ""
}
