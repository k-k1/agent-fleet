package main

// engines.go — the Workspace side of the fleet's own inference engines (ADR 0071 P0).
//
// Four small jobs, all of them talking to the Control Plane over the same public hairpin
// the memo, schedule and MCP bridges use (AF_CP_BASE_URL), authenticated by the issuing
// token the CP injects at container start (AF_ENGINE_ISSUE_TOKEN):
//
//  1. at boot, ask which engines exist and write the CHAT ones into opencode's config as a
//     provider, so `llamacpp/<model>` is in the launch menu — while every engine is asleep;
//  2. at each opencode launch, buy a token scoped to THAT session and hand it to the pane
//     through tmux's environment;
//  3. tell the image generation layer how to reach the IMAGES engine, which is the transport
//     behind the `sdcpp` provider (ADR 0071 P1, ADR 0069 decision 3);
//  4. accept the usage rows the CP posts back after an engine answers, and append them to
//     this workspace's ledger, which is where every other feature's consumption lives.
//
// A deployment with no engines gets 404 on the catalog and everything here is a no-op. That
// is the normal case: a GPU box is $1.26/hour and nobody deploys one by accident.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var engineHTTP = &http.Client{Timeout: 20 * time.Second}

// The opencode launcher calls this for every session it starts. Wired here rather than in
// the opencode package because minting the token is a call to the Control Plane, and an
// internal CLI package has no business knowing the CP exists (the same seam UsagePref uses).
//
// The imagegen seam is the same shape and exists for the same reason: internal/imagegen owns
// "make pixels", not "know where the Control Plane is".
func init() {
	opencode.EngineEnv = engineSessionEnv
	imagegen.EngineLookup = engineImageConn
	imagegen.EngineImageRows = engineImageProviderRows
	// Generated pictures land in the gallery's own folder, so pay the decode here rather
	// than when somebody opens it (fs_thumb.go's warming notes). 512 is the edge every
	// surface asks for — the mirror's cards, the gallery's, the studio's.
	imagegen.WarmThumb = func(p string) { warmThumbFile(p, 512) }
	// The path gate is the same kind of seam (ADR 0081 decision 3): the browse root, the
	// denylist and the symlink re-check are fs.go's, and internal/imagegen owns "make pixels",
	// not "know which folders this workspace may read and write". Re-implementing either check
	// inside that package would be a second copy of a security decision.
	imagegen.BrowsePath = safeBrowsePath
	imagegen.BrowseWritablePath = safeWritableBrowsePath
	// Which Agent wrote a graph is provenance the sidecar carries and nothing else can: the
	// templates change between releases and the record outlives the binary.
	imagegen.Build = buildVersion
	// ADR 0093 phase 1's LLM client (internal/harness) is the same seam shape again: it must
	// never learn a Control Plane exists, so it gets the connection, the window and the
	// availability bit through func-vars rather than reading AF_CP_BASE_URL itself.
	harness.EngineToken = harnessEngineToken
	harness.EngineWindow = harnessEngineWindow
	harness.EngineAvailable = harnessEngineAvailable
}

// The API families the CP's catalogue reports. Chat engines become opencode providers; images
// engines become an imagegen provider. The key is NOT what decides this — an engine's role is
// declared by the stack (ADR 0071 decision 8).
const (
	engineAPIChat   = "chat"
	engineAPIImages = "images"
)

// knownImageProviders is the images-API vocabulary this Agent build actually implements a
// client for (ADR 0083 decision 5). A row naming anything else cannot be served no matter what
// the catalogue says about it — imagegen.EngineLookup simply never matches it — and that used to
// be entirely silent: generate_image just never reached tools/list, with no error and no log.
var knownImageProviders = map[string]bool{
	imagegen.ProviderComfy:        true,
	imagegen.ProviderOpenAICompat: true,
}

// logUnservableImageRows says, once per fresh catalogue fetch, which images rows this build
// cannot serve — the Agent side of decision 5's refusal. Called only when engineCatalogRows
// actually went to the network, not on every cache hit off the 10-minute TTL, so a deployment
// running an unserved row is not asked to read the same line every tools/list.
func logUnservableImageRows(rows []engineCatalogRow) {
	for _, e := range rows {
		if e.api() != engineAPIImages || knownImageProviders[e.Provider] {
			continue
		}
		log.Printf("engines: %s declares images provider %q, which this Agent build does not implement (known: comfy, openai-compat) — generate_image will not reach it", e.Key, e.Provider)
	}
}

// engineCatalogRow is one engine as the CP describes it. It never touches an engine to
// answer, which is what lets the launch menu be drawn — and the image tool be advertised —
// while every GPU box is asleep.
type engineCatalogRow struct {
	Key      string   `json:"key"`
	API      string   `json:"api"`
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url"` // relative to AF_CP_BASE_URL
	Models   []string `json:"models"`
	// The window the engine was STARTED with, and the output cap declared alongside it. Both
	// absent (0) on a deployment whose engine stack predates them, which is why they are
	// passed on rather than defaulted — see opencode.EngineProvider.
	ContextTokens   int `json:"context_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
	// ModelRows is the same models with what the CATALOGUE says about each (ADR 0072): its own
	// window, the line an agent reads when it chooses, and — for the image role — the sizes the
	// checkpoint was trained at, DECLARED rather than guessed from the id.
	//
	// Absent on a Control Plane older than the catalogue, which is why Models above stays: the
	// two images are deployed separately and the Agent is upgraded on its own schedule.
	ModelRows []engineCatalogModel `json:"model_rows"`
	// Loras are the accessories, never something to start the engine with. Carried for the
	// image role so generate_image can offer them by name (ADR 0072 decision 5, phase P3).
	Loras []engineCatalogModel `json:"loras"`
	// NegativeAlways is what this engine's administrator excludes from every image, whoever asks
	// and whichever model answers (ADR 0072 follow-up, negative prompts). Empty on a Control
	// Plane older than that follow-up, which is the same as nothing being configured.
	NegativeAlways string `json:"negative_always"`
}

// engineCatalogModel is one model as the catalogue describes it.
type engineCatalogModel struct {
	ID string `json:"id"`
	// Label is what a MEMBER is shown instead of the id (ADR 0090) — the publisher's name and
	// the one part that tells two sizes of one model apart, composed by the Control Plane
	// because only that side holds all three pieces.
	//
	// 🔴 A name to DRAW and never a name to send. `ID` stays what opencode keys the model by,
	// what a request names and what the gateway routes on. Empty on a Control Plane that does
	// not compose it yet, and on a row nobody has read a model page for — every reader then
	// falls back to the id, which is what they all did before this field existed.
	Label           string   `json:"label"`
	ContextTokens   int      `json:"context_tokens"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Description     string   `json:"description"`
	Sizes           []string `json:"sizes"`
	BaseModel       string   `json:"base_model"`
	// Negative is this row's own recommended negative prompt, added to every request that names
	// this model. Empty for a row that declares none.
	Negative string `json:"negative"`
	Selected bool   `json:"selected"`
	Default  bool   `json:"default"`
	// Files are the on-disk basenames ADR 0072 decision 2 declares, only sent for the image
	// role's non-LoRA models — the comfy provider (ADR 0072 P2) is the first reader; sdcpp
	// never asked because it holds one checkpoint and never chooses which file to load.
	Files []engineCatalogFile `json:"files"`
	// Params is what the catalogue row declares about HOW to run this model — steps, cfg,
	// sampler, scheduler, and a LoRA's strength. Absent for a row that declares nothing, which
	// is the provider's own family recipe and the normal case.
	//
	// A pointer so "the row said nothing" and "the row said zero" stay apart: the provider
	// merges these over its template field by field, and a zero-valued struct would erase the
	// recipe instead of leaving it alone.
	Params *imagegen.EngineParams `json:"params"`
	// Warm is whether the Control Plane last saw the engine actually answer with THIS model
	// (ADR 0072 decision 7's warm_model). At most one row per engine has it true.
	Warm bool `json:"warm"`
	// TrainedWords are a LoRA row's trigger words (ADR 0081 decision 5), and the three licence /
	// source fields say what a checkpoint's weights were published under and where they came from.
	//
	// All four are ABSENT on a Control Plane that does not relay them yet, and are read as empty
	// rather than waited for: the two images are deployed separately and this Agent must keep
	// answering while only one of the pair has landed. That is the lesson sessionWire taught —
	// a field missing from the relay vanishes silently — which is why these are named here, in
	// the reader, even before the relay carries them.
	TrainedWords []string `json:"trained_words"`
	LicenseName  string   `json:"license_name"`
	LicenseURL   string   `json:"license_url"`
	SourceURL    string   `json:"source_url"`
}

// engineCatalogFile is one file of engineCatalogModel — see imagegen.EngineFile, which this is
// translated into. The wire keeps sd.cpp's own flag spelling (control-plane/engine_catalog.go),
// so a reader does not need a second vocabulary for a fact the catalogue already states once.
type engineCatalogFile struct {
	Flag  string `json:"flag"`
	S3Key string `json:"s3_key"`
}

// api defaults to chat, matching the CP's own reading of a table written before the field
// existed.
func (r engineCatalogRow) api() string {
	if v := strings.TrimSpace(r.API); v != "" {
		return v
	}
	return engineAPIChat
}

// The catalogue is cached because Ready() is on the tools/list path — a client asks it at the
// start of every turn — and a CP round trip there would be paid for by every session on every
// turn. The TTL is long because the answer only changes when the 60-engines stack does.
const (
	engineCatalogTTL      = 10 * time.Minute
	engineCatalogRetryTTL = time.Minute // after a failure: back off, but notice a CP that came back
)

var engineCatalogState struct {
	mu   sync.Mutex
	rows []engineCatalogRow
	at   time.Time
	ok   bool
}

// engineCatalogRows returns the engines this deployment runs, refreshing at most once per
// TTL. A failed refresh keeps the previous answer rather than reporting "no engines": the
// engines did not go away because the hairpin blipped, and dropping them would take
// generate_image out of tools/list mid-conversation.
func engineCatalogRows(ctx context.Context) []engineCatalogRow {
	engineCatalogState.mu.Lock()
	defer engineCatalogState.mu.Unlock()
	ttl := engineCatalogTTL
	if !engineCatalogState.ok {
		ttl = engineCatalogRetryTTL
	}
	if !engineCatalogState.at.IsZero() && time.Since(engineCatalogState.at) < ttl {
		return engineCatalogState.rows
	}
	var cat struct {
		Engines []engineCatalogRow `json:"engines"`
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	engineCatalogState.at = time.Now()
	if !engineCPCall(c, http.MethodGet, "/internal/engine/catalog", nil, &cat) {
		engineCatalogState.ok = false
		return engineCatalogState.rows
	}
	engineCatalogState.rows, engineCatalogState.ok = cat.Engines, true
	logUnservableImageRows(cat.Engines)
	return engineCatalogState.rows
}

// engineCPCall is the shared shape of the two calls out: base URL, issuing token, JSON in
// and out. Returns ok=false with no error logged for "this deployment has no engines" (404)
// and for "there is no CP to ask" (a dev agent with no AF_CP_BASE_URL).
func engineCPCall(ctx context.Context, method, path string, in, out any) bool {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	token := strings.TrimSpace(os.Getenv("AF_ENGINE_ISSUE_TOKEN"))
	if base == "" || token == "" {
		return false
	}
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return false
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := engineHTTP.Do(req)
	if err != nil {
		log.Printf("engines: %s %s failed: %v", method, path, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false // no engines in this deployment: not a problem, and not worth a line
	}
	if resp.StatusCode >= 300 {
		log.Printf("engines: %s %s answered %s", method, path, resp.Status)
		return false
	}
	if out == nil {
		return true
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// syncEngineProviders asks the CP for the engine catalogue and writes it into opencode's
// config. Called once at boot: the catalogue only changes when the 60-engines stack does,
// and that replaces the CP task, whose next workspace start runs this again.
func syncEngineProviders() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows := engineCatalogRows(ctx)
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	providers := make([]opencode.EngineProvider, 0, len(rows))
	for _, e := range rows {
		// CHAT engines only. An image engine declared here would put `sdcpp/sdxl-base-1.0`
		// in the launch menu as something to hold a conversation with, and picking it would
		// send a chat completion to an endpoint that has never heard of one.
		if e.api() != engineAPIChat {
			continue
		}
		ctxTokens, maxOut, windows := e.ContextTokens, e.MaxOutputTokens, engineModelWindows(e)
		// The one bounce ADR 0093's phase 0 adds (decision 7, docs/log/99 §4.11): while the box
		// is warm enough to answer, the window it actually started with replaces the catalogue's
		// declared one — for the ONE model that answer describes. Asleep, forbidden, or a Control
		// Plane too old for the route all answer 0 here, and the catalogue's number is left
		// standing exactly as before this existed.
		if id, nctx := engineWarmWindow(e); nctx > 0 {
			if id == "" {
				// No model row to pin the measurement to (an engine with no model_rows at all) —
				// the engine-wide fallback is the only number this row has, so that is what gets
				// corrected.
				ctxTokens = nctx
			} else {
				// A model row exists, and per-model ALWAYS wins over the engine-wide fallback
				// (engineProviderEntry's own rule). Writing only windows[id] keeps every OTHER
				// model on this router at its own declared window — /props described this one
				// model, not the others, and the engine-wide ctxTokens must not move on the
				// strength of a measurement that was never about it.
				out := maxOut
				if w, ok := windows[id]; ok && w.MaxOutputTokens > 0 {
					out = w.MaxOutputTokens
				}
				if windows == nil {
					windows = map[string]opencode.EngineModelWindow{}
				}
				windows[id] = opencode.EngineModelWindow{ContextTokens: nctx, MaxOutputTokens: out}
			}
		}
		providers = append(providers, opencode.EngineProvider{
			Key: e.Key, Provider: e.Provider, BaseURL: base + e.BaseURL, Models: e.Models,
			ContextTokens: ctxTokens, MaxOutputTokens: maxOut,
			Windows: windows,
			Labels:  engineModelLabels(e),
		})
	}
	changed, removed, err := opencode.WriteEngineProviders(providers)
	if err != nil {
		log.Printf("engines: writing the opencode provider failed: %v", err)
		return
	}
	if !changed {
		return
	}
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Provider+" ("+strings.Join(p.Models, ",")+")")
	}
	log.Printf("engines: opencode provider written: %s", strings.Join(names, "; "))
	// A serve daemon that was already up read its config at start, so the file alone does not
	// reach it. Hand it over live where that is possible — this runs on every catalogue push
	// the Control Plane makes, and replacing the daemon instead would cut short whatever turn
	// is in flight in this workspace.
	if !removed {
		err := opencode.PushEngineProviders(providers)
		if err == nil || errors.Is(err, opencode.ErrNoDaemon) {
			return // applied live, or there is no daemon holding a stale copy
		}
		log.Printf("engines: handing the provider block to serve failed, asking for a restart instead: %v", err)
	}
	// A removal cannot be patched into a running daemon (PushEngineProviders), and a failed
	// patch leaves it stale either way: tell the user, and let them pick the moment.
	opencode.ApplyEngineChange(strings.Join(names, "; "))
}

// engineModelWindows is the per-model context/output declaration, or nil when the Control
// Plane sent none — in which case opencode gets the engine-wide pair, exactly as before.
// engineModelLabels is the member-facing name per model id, for opencode's own model picker
// (ADR 0090). nil when the Control Plane composes none.
func engineModelLabels(e engineCatalogRow) map[string]string {
	out := map[string]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && m.Label != "" {
			out[m.ID] = m.Label
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func engineModelWindows(e engineCatalogRow) map[string]opencode.EngineModelWindow {
	if len(e.ModelRows) == 0 {
		return nil
	}
	out := map[string]opencode.EngineModelWindow{}
	for _, m := range e.ModelRows {
		if m.ID == "" || m.ContextTokens <= 0 {
			continue
		}
		out[m.ID] = opencode.EngineModelWindow{
			ContextTokens: m.ContextTokens, MaxOutputTokens: m.MaxOutputTokens,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// enginePropsTimeout bounds ONE read of the window bounce. Short, and deliberately shorter than
// engineCatalogRows' own 15 s: this runs once per CHAT engine on every sync, and a box that is
// not answering must not turn a catalogue refresh into a multi-engine deployment's worth of
// waiting — the route behind it never waits for a start either (control-plane/engine_gateway.go's
// props(), ADR 0093 decision 7).
const enginePropsTimeout = 10 * time.Second

// engineMeasuredWindows remembers the last window ACTUALLY read from /props, per (engine key,
// model id). A box that has gone back to sleep, a Control Plane too old for the route, or a
// tenant that just lost the role all make engineWarmWindow answer 0 on THIS call — but the
// number measured while the box WAS warm has not become a lie because it stopped answering, and
// reverting to the catalogue's declared value on every sleep would rewrite opencode's config
// (and, through WriteEngineProviders' before/after compare, restart `serve`) on the GPU's own
// idle schedule rather than on anything about the model that changed.
var engineMeasuredWindows sync.Map // engineWindowKey -> int

type engineWindowKey struct{ key, id string }

// engineWarmWindow asks the Control Plane's read-only `GET /engine/{key}/props` bounce (ADR
// 0093 decision 7, docs/log/99 §4.11) for the window llama-server actually started with, and
// which model that answer describes — falling back to the last value actually measured for that
// (key, model) pair when the box is not answering right now (engineMeasuredWindows).
//
// The id defaults to the catalogue's own guess — selected, else default, else the first row,
// mirroring the Control Plane's engineStartWindow — but a LIVE probe overrides that guess with
// whatever id /props actually said the window belongs to (enginePropsProbe.id). That override is
// the fix for a real bug (2026-09-20, sandbox deployment): a router's own max_instances: 1 means
// only one model is ever loaded at a time, and it does not have to be the catalogue's declared
// default — the guess is exactly that, a guess, and the live answer is the one caller loaded.
// Without the override, a window measured for a non-default model would still get filed under
// the WRONG (guessed) id, mislabeling one model's window as another's. probe.id is "" for a
// single-model /props (default_generation_settings.n_ctx alone, no router fields at all), where
// there is no ambiguity to correct in the first place — the catalogue's guess is the only model
// there is.
//
// nctx is 0 only when there is NOTHING to correct: a provider other than llamacpp (the only one
// with a /props to read — comfy and sdcpp have none), or an engine that has never once answered
// this process. Neither waits for anything: the CP's props() never runs ensureReady behind this
// call, so a box that is asleep answers in milliseconds, not minutes.
//
// Deliberately its own budget rather than the caller's ctx: syncEngineProviders' ctx is one 30 s
// allowance for the WHOLE catalogue fetch, shared across however many chat engines it lists, and
// chaining this off it would starve a later engine's read the moment an earlier one is slow —
// which reads here exactly like "asleep", and is not. enginePropsWindow and the token mint
// inside it carry their own bounds (10 s and 15 s), so this still never waits without limit.
//
// engineMeasuredWindows is still keyed by the id ACTUALLY reported (not the guess) so a later
// read of that same model's window (while it stays loaded) hits the right entry. A sleeping box,
// or a Control Plane too old to report an id, falls back to the catalogue's guess instead — this
// process has no other way to know which model a silent box last had loaded, and reverting to
// the declared value for that case is the same trade decision 7 already made, not a new one.
func engineWarmWindow(e engineCatalogRow) (id string, nctx int) {
	if e.Provider != "llamacpp" {
		return "", 0
	}
	guessed := engineSelectedModelID(e.ModelRows)
	if probe := enginePropsWindow(context.Background(), e.Key); probe.nctx > 0 {
		id = guessed
		if probe.id != "" {
			id = probe.id
		}
		engineMeasuredWindows.Store(engineWindowKey{key: e.Key, id: id}, probe.nctx)
		return id, probe.nctx
	}
	if v, ok := engineMeasuredWindows.Load(engineWindowKey{key: e.Key, id: guessed}); ok {
		return guessed, v.(int)
	}
	return guessed, 0
}

// engineSelectedModelID mirrors control-plane/engine_gateway.go's engineStartWindow: the
// selected or default row, falling back to the first one. Used as engineWarmWindow's starting
// guess, and its only source of truth for a single-model engine or a sleeping/old-CP fallback —
// a LIVE router reading overrides it (see engineWarmWindow's own doc comment).
func engineSelectedModelID(rows []engineCatalogModel) string {
	first := ""
	for _, m := range rows {
		if m.ID == "" {
			continue
		}
		if first == "" {
			first = m.ID
		}
		if m.Selected || m.Default {
			return m.ID
		}
	}
	return first
}

// enginePropsProbe is what enginePropsWindow actually read from one /props call: the window, and
// — for a router deployment — the id it describes. id is "" for a single-model /props (or a
// clean 0, meaning nothing was read at all), because a single declared model needs no name: it
// is the one that was running by definition.
type enginePropsProbe struct {
	id   string
	nctx int
}

// enginePropsWindow is the one HTTP call behind engineWarmWindow. A zero-value probe on anything
// that is not a clean 200 with a readable body — a stopped engine's 503, a tenant this
// membership was denied, no Control Plane to ask (AF_CP_BASE_URL unset, same as engineCPCall) —
// all read the same here: nothing to correct.
//
// Not routed through engineCPCall: that helper's bearer is the workspace's ISSUING token, and
// `/engine/{key}/props` is session-token gated like every other `/engine/{key}/...` route
// (control-plane/engine_gateway.go's props(), same claims.Key check serve() makes). The token is
// WORKSPACE-scoped (the same trade engineImageConn makes, and for the same reason): this runs
// once at boot and again on every catalogue push, never per session.
func enginePropsWindow(ctx context.Context, key string) enginePropsProbe {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	if base == "" {
		return enginePropsProbe{}
	}
	tok := engineToken(ctx, key, "")
	if tok == "" {
		return enginePropsProbe{}
	}
	c, cancel := context.WithTimeout(ctx, enginePropsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, base+"/engine/"+key+"/props", nil)
	if err != nil {
		return enginePropsProbe{}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := engineHTTP.Do(req)
	if err != nil {
		return enginePropsProbe{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return enginePropsProbe{} // asleep (engine_unavailable/engine_off), forbidden, or an old CP
	}
	var out struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		// RouterModels is the current shape (control-plane/engine_gateway.go's
		// enginePropsAugmentRouterWindow): every id GET {base}/v1/models actually lists, with the
		// window it reported for each — never a guess at which one is "selected". A router's own
		// max_instances: 1 means this is normally exactly one entry; the first (and, in practice,
		// only) one is what this call was actually able to measure.
		RouterModels []struct {
			ID   string `json:"id"`
			NCtx int    `json:"n_ctx"`
		} `json:"router_models"`
		// RouterSelectedModel is the OLDER shape (ADR 0093 段0 追补's original addition), read as a
		// fallback for a BORROWED row whose lending deployment's Control Plane predates the
		// router_models fix above (the version-skew case this side cannot itself close). Its id is
		// only ever the catalogue's own guess, not a live one, but that guess is also the only thing
		// an old CP was ever capable of reporting successfully in the first place.
		RouterSelectedModel struct {
			ID   string `json:"id"`
			NCtx int    `json:"n_ctx"`
		} `json:"router_selected_model"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out) != nil {
		return enginePropsProbe{}
	}
	if out.DefaultGenerationSettings.NCtx > 0 {
		return enginePropsProbe{nctx: out.DefaultGenerationSettings.NCtx}
	}
	if len(out.RouterModels) > 0 {
		return enginePropsProbe{id: out.RouterModels[0].ID, nctx: out.RouterModels[0].NCtx}
	}
	if out.RouterSelectedModel.NCtx > 0 {
		return enginePropsProbe{id: out.RouterSelectedModel.ID, nctx: out.RouterSelectedModel.NCtx}
	}
	return enginePropsProbe{}
}

// handleEngineCatalogChanged (POST /engine/catalog-changed) is the Control Plane telling this
// workspace that the model catalogue moved (ADR 0072 decision 7).
//
// The catalogue is cached for ten minutes because it is read on the tools/list path — every
// turn, of every session — so without this push an administrator's new checkpoint would take
// up to that long to appear in a launch menu. The TTL stays as the safety net: a workspace that
// was starting, or unreachable, or created after the change, converges on its own.
//
// Called by the CP itself, like /engine/usage — never by the Console, so it needs no entry in
// the CP's agent-proxy allowlist.
func handleEngineCatalogChanged(w http.ResponseWriter, r *http.Request) {
	// Drop the cache so the refresh below really re-reads rather than returning the copy that
	// is up to ten minutes old.
	engineCatalogState.mu.Lock()
	engineCatalogState.at = time.Time{}
	engineCatalogState.mu.Unlock()
	// Detached from the request: syncEngineProviders dials the CP and then rewrites opencode's
	// config, and the CP is not waiting for either.
	go syncEngineProviders()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"refreshing": true})
}

// --- the per-session token ------------------------------------------------------

// engineTokenCache holds one token per (engine key, scope) — the scope being a session name,
// or "" for the workspace-wide one the managed route's shared daemon and the image tool use. A
// launch is not the only thing that asks (`opencode models` asks on every launch-modal open),
// and buying a new token each time would leave a trail of live credentials behind one session.
//
// Keyed by engine as well as by scope because a token opens ONE engine: presenting the llm
// token to /engine/image/v1 is refused, and it should be — that is the claim doing its job.
var engineTokenCache sync.Map // engineTokenScope -> engineCachedToken

type engineTokenScope struct {
	key     string // the engine, e.g. "llm" or "image"
	session string // "" = workspace-scoped
}

type engineCachedToken struct {
	value string
	// renewAt is deliberately well before the token's own expiry: a session relaunched at
	// the last minute must not be handed a credential that dies inside the first answer.
	renewAt time.Time
	// failedAt marks a negative entry. This is called from `opencode models`, which is on the
	// launch modal's path, so a Control Plane that cannot be reached must cost one timeout and
	// not one per modal open.
	failedAt time.Time
}

// engineNegativeCache is how long a failed mint is remembered. Short enough that a CP which
// has just come back is picked up on the next launch, long enough that a modal opened
// repeatedly does not stall repeatedly.
const engineNegativeCache = time.Minute

// engineSessionEnv mints (or reuses) an engine token for `name`, as KEY=VALUE entries.
//
// An EMPTY name asks for the workspace-scoped token, which is what the managed route needs:
// its `opencode serve` daemon is shared by every session in the workspace, so there is no
// session to scope it to. A named session gets a session-scoped one.
//
// Nil when the deployment runs no engines, which is what makes it safe to call
// unconditionally from the launcher and from env().
func engineSessionEnv(name string) []string {
	tok := engineToken(context.Background(), "llm", name)
	if tok == "" {
		return nil
	}
	return []string{opencode.EngineProviderKeyEnv + "=" + tok}
}

// engineToken mints (or reuses) a token for one engine and one scope. "" when the deployment
// runs no engines, when the CP cannot be reached, or when that engine does not exist.
func engineToken(ctx context.Context, key, session string) string {
	scope := engineTokenScope{key: key, session: session}
	if v, ok := engineTokenCache.Load(scope); ok {
		c := v.(engineCachedToken)
		if !c.failedAt.IsZero() && time.Since(c.failedAt) < engineNegativeCache {
			return ""
		}
		if c.value != "" && time.Now().Before(c.renewAt) {
			return c.value
		}
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	req := map[string]string{"session": session, "key": key}
	if !engineCPCall(c, http.MethodPost, "/internal/engine/token", req, &out) || out.Token == "" {
		engineTokenCache.Store(scope, engineCachedToken{failedAt: time.Now()})
		return ""
	}
	renew := time.Now().Add(time.Hour)
	if exp, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		if half := time.Until(exp) / 2; half > 0 {
			renew = time.Now().Add(half)
		}
	}
	engineTokenCache.Store(scope, engineCachedToken{value: out.Token, renewAt: renew})
	return out.Token
}

// --- the image engine (ADR 0071 P1) ----------------------------------------------

// engineImageProviderRows lists the images rows Providers() should construct a client for: one
// per catalogue row this Agent build actually implements a client for (knownImageProviders),
// keyed by the row's OWN key rather than its provider field (ADR 0082 decision 1) — that key is
// what becomes the provider's id everywhere a caller, the stored order, the ledger and the MCP
// surface name it.
func engineImageProviderRows(ctx context.Context) []imagegen.EngineImageRow {
	rows := engineCatalogRows(ctx)
	out := make([]imagegen.EngineImageRow, 0, len(rows))
	for _, e := range rows {
		if e.api() != engineAPIImages || !knownImageProviders[e.Provider] {
			continue
		}
		out = append(out, imagegen.EngineImageRow{Key: e.Key, Provider: e.Provider})
	}
	return out
}

// engineImageConn tells internal/imagegen how to reach one images engine row, keyed by the row's
// OWN key (ADR 0082 decision 1) rather than by provider kind — two rows of the same kind (a
// managed comfy engine and an operator's LAN comfy box) must each reach the URL that is actually
// theirs. Called from the provider's Ready() (the tools/list path) and from its Generate, so both
// sides of "is it offered" and "can it run" agree by construction.
//
// The token is WORKSPACE-scoped, not per session, and that is a different trade from
// opencode's. The engine credential opencode uses ends up in a config the model can read
// through `{env:…}`, so a narrow scope limits what a leak costs; this one never leaves the
// Agent's own process. What the CP does with the session claim is label a usage row, and for
// images the row is written here instead (feature tool.imagegen, with the session as its ref),
// so a session-scoped token would buy nothing and cost one credential per session.
func engineImageConn(ctx context.Context, key string) (imagegen.EngineConn, bool) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	if base == "" {
		return imagegen.EngineConn{}, false
	}
	for _, e := range engineCatalogRows(ctx) {
		if e.api() != engineAPIImages || e.Key != key {
			continue
		}
		tok := engineToken(ctx, e.Key, "")
		if tok == "" {
			return imagegen.EngineConn{}, false
		}
		return imagegen.EngineConn{
			BaseURL:      base + e.BaseURL,
			Token:        tok,
			Models:       engineImageModelIDs(e),
			Sizes:        engineImageSizes(e),
			BaseModel:    engineImageBaseModels(e),
			Files:        engineImageFiles(e),
			Warm:         engineImageWarm(e),
			Labels:       engineImageLabels(e),
			Descriptions: engineImageDescriptions(e),
			Negatives:    engineImageNegatives(e),
			// Trimmed here rather than at every reader: the administrator types this into a text
			// box, and a value of one space would otherwise compose into a negative prompt with a
			// stray comma in it.
			NegativeAlways: strings.TrimSpace(e.NegativeAlways),
			Loras:          engineImageLoras(e),
			Params:         engineImageParams(e),
			Licenses:       engineImageLicenses(e),
		}, true
	}
	return imagegen.EngineConn{}, false
}

// engineImageModelIDs puts the SELECTED checkpoint first. sd-server holds one, chosen by a
// startup flag, so the first id is the provider's default model and the one its Caps describe
// — and after ADR 0072 which one that is, is an administrator's choice rather than the order
// the stack happened to list them in.
func engineImageModelIDs(e engineCatalogRow) []string {
	out := make([]string, 0, len(e.Models))
	for _, m := range e.ModelRows {
		if m.Selected || m.Default {
			out = append(out, m.ID)
		}
	}
	for _, id := range e.Models {
		if len(out) > 0 && out[0] == id {
			continue
		}
		out = append(out, id)
	}
	return out
}

// engineImageSizes is the declared size list per model id, or nil when the catalogue says
// nothing — in which case the provider goes on reading the id, as it always has.
func engineImageSizes(e engineCatalogRow) map[string][]string {
	out := map[string][]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && len(m.Sizes) > 0 {
			out[m.ID] = m.Sizes
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageBaseModels is the declared checkpoint family per model id (ADR 0072 decision 2),
// which the comfy provider (ADR 0072 P2) reads to pick a workflow template. nil on a catalogue
// that declares no family at all — sdcpp never reads this field, so there is nothing to degrade.
func engineImageBaseModels(e engineCatalogRow) map[string]string {
	out := map[string]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && m.BaseModel != "" {
			out[m.ID] = m.BaseModel
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageNegatives is the catalogue's own negative prompt per model id (ADR 0072 follow-up,
// negative prompts) — what a checkpoint's publisher recommends keeping out, which for the SDXL
// fine-tunes is half of what makes the model behave as its sample pictures do. nil when no row
// declares one, which is every catalogue written before the column existed.
func engineImageNegatives(e engineCatalogRow) map[string]string {
	out := map[string]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && strings.TrimSpace(m.Negative) != "" {
			out[m.ID] = strings.TrimSpace(m.Negative)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageFiles is the declared file list per model id, translated from the wire's sd.cpp
// flag spelling into imagegen.EngineFile — see EngineFile's own comment for why comfy re-reads
// that vocabulary rather than a second one. nil on a catalogue with no files declared, which is
// every catalogue sdcpp alone has ever needed to read.
func engineImageFiles(e engineCatalogRow) map[string][]imagegen.EngineFile {
	out := map[string][]imagegen.EngineFile{}
	for _, m := range e.ModelRows {
		if m.ID == "" || len(m.Files) == 0 {
			continue
		}
		files := make([]imagegen.EngineFile, 0, len(m.Files))
		for _, f := range m.Files {
			name := f.S3Key
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			if name == "" {
				continue
			}
			files = append(files, imagegen.EngineFile{Flag: f.Flag, Name: name})
		}
		if len(files) > 0 {
			out[m.ID] = files
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageLoras is the enabled LoRAs the catalogue declares for this engine (ADR 0072
// decision 5, phase P3). They ride a list of their OWN on the wire, next to model_rows rather
// than in it, because a LoRA is not something an engine can be started with — which is also why
// they never reach generate_image's `model` enum: engineImageModelIDs reads models/model_rows
// and the Control Plane puts a LoRA row in neither (control-plane/engine_gateway.go).
//
// The name sent to the engine is the file's basename, the same rule engineImageFiles follows:
// the box mirrors bucket keys onto disk verbatim, and `image/loras/x.safetensors` is what
// ComfyUI lists as `x.safetensors` under its own models directory. A row with no file is dropped
// — it could be named and never loaded.
func engineImageLoras(e engineCatalogRow) []imagegen.EngineLora {
	out := make([]imagegen.EngineLora, 0, len(e.Loras))
	for _, m := range e.Loras {
		if m.ID == "" {
			continue
		}
		name := ""
		for _, f := range m.Files {
			if i := strings.LastIndex(f.S3Key, "/"); i >= 0 {
				name = f.S3Key[i+1:]
			} else {
				name = f.S3Key
			}
			if name != "" {
				break
			}
		}
		if name == "" {
			continue
		}
		lora := imagegen.EngineLora{
			ID: m.ID, File: name, Description: m.Description, BaseModel: m.BaseModel,
			TrainedWords: m.TrainedWords,
		}
		if m.Params != nil {
			lora.Weight = m.Params.Weight
		}
		out = append(out, lora)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageParams is the declared generation defaults per model id. Only the rows that
// declare something appear, so a missing key means "use the family's recipe" and never
// "declared all zeros".
func engineImageParams(e engineCatalogRow) map[string]imagegen.EngineParams {
	out := map[string]imagegen.EngineParams{}
	for _, m := range e.ModelRows {
		if m.ID != "" && m.Params != nil && *m.Params != (imagegen.EngineParams{}) {
			out[m.ID] = *m.Params
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageLicenses is what each model's weights were published under and where they came from
// (ADR 0081 decision 5). nil when no row carries any of the three, which is every catalogue from
// a Control Plane that does not relay them yet — the member-facing catalogue then shows nothing
// rather than an empty licence, because "no licence stated" and "licence unknown to this Agent"
// are different things to tell somebody about to publish a picture.
func engineImageLicenses(e engineCatalogRow) map[string]imagegen.EngineLicense {
	out := map[string]imagegen.EngineLicense{}
	for _, m := range e.ModelRows {
		lic := imagegen.EngineLicense{
			Name:   strings.TrimSpace(m.LicenseName),
			URL:    strings.TrimSpace(m.LicenseURL),
			Source: strings.TrimSpace(m.SourceURL),
		}
		if m.ID != "" && lic != (imagegen.EngineLicense{}) {
			out[m.ID] = lic
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageWarm is the id of the model the Control Plane last saw this engine actually answer
// with (ADR 0072 decision 7's warm_model), or "" when nothing is known to be warm — a
// just-started engine, or a catalogue from a CP that predates the field.
func engineImageWarm(e engineCatalogRow) string {
	for _, m := range e.ModelRows {
		if m.Warm {
			return m.ID
		}
	}
	return ""
}

// engineImageLabels is the member-facing name per model id (ADR 0090). nil when the Control
// Plane composes none, which is what every reader treats as "draw the id", as before.
func engineImageLabels(e engineCatalogRow) map[string]string {
	out := map[string]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && m.Label != "" {
			out[m.ID] = m.Label
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// engineImageDescriptions is the catalogue's own per-model line (ADR 0072 decision 2) — the
// sentence an agent reads when choosing a checkpoint. nil when the catalogue declares none.
func engineImageDescriptions(e engineCatalogRow) map[string]string {
	out := map[string]string{}
	for _, m := range e.ModelRows {
		if m.ID != "" && m.Description != "" {
			out[m.ID] = m.Description
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- llama.cpp: the member's own LAN connection (docs/log/107) ------------------------
//
// A member can point THEIR lcpp sessions straight at a LAN llama-server (or router),
// bypassing the deployment's own "llm" engine role and the Control Plane entirely — which
// also means the tenant's allow_engine_llm gate (ADR 0084) has nothing to say about a direct
// connection; the guide says so in plain language rather than leaving it to be discovered.
// secrets.Data.Lcpp holds the connection (URL always normalized without a trailing "/v1",
// connections.go's normalizeLcppURL); harnessEngineToken/Window/Available below and
// lcppModels (agent_models.go) all check it FIRST, falling back to the deployment's own
// catalogue-backed path (engineCatalogRows) only when it is unset — an empty connection
// means today's behavior, unchanged (docs/log/107 decision 1).
//
// lcppMemberClient is deliberately its own short-timeout client, separate from engineHTTP's
// 20s: every call here is a synchronous UI action (building a launch menu, or the settings
// card's "check connection" button) against a LAN box that may simply be off or asleep, and
// must fail fast rather than hang the caller — the same lesson opencode's 10s enumeration
// timeout taught when it blanked the whole launch menu (docs/log/54).
var lcppMemberClient = &http.Client{Timeout: 3 * time.Second}

// lcppMemberBase strips a trailing "/v1" the same way normalizeLcppURL (connections.go)
// already did before storing — belt and suspenders, since nothing enforces that every future
// writer of secrets.LcppConn goes through that one path.
func lcppMemberBase(url string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(url), "/"), "/v1")
}

// harnessMemberConn reads the member's own lcpp connection, if any is configured. ok is
// false on a store error or an empty/unset URL — both read as "no member connection",
// falling through to the deployment's own engine.
func harnessMemberConn() (secrets.LcppConn, bool) {
	s, err := secrets.Load()
	if err != nil || s.Lcpp == nil || strings.TrimSpace(s.Lcpp.URL) == "" {
		return secrets.LcppConn{}, false
	}
	return *s.Lcpp, true
}

// lcppMemberProps is GET {base}/props's relevant shape — the same fields
// workspace/agent/internal/harness/live_contract_test.go's propsResponse pins against a real
// llama-server, minus Role/ModelPath (this side never needs to tell router from single-model
// apart; lcppMemberWindow's /props-then-/v1/models fallback below handles both the same way
// enginePropsAugmentRouterWindow does for the CP-proxied path).
type lcppMemberProps struct {
	BuildInfo                 string `json:"build_info"`
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// lcppMemberProbeProps reads /props directly against the member's own connection — never
// through the Control Plane, which this connection bypasses entirely. ok is false on
// anything but a clean 200 with a readable body: the box is asleep, off, or unreachable.
func lcppMemberProbeProps(ctx context.Context, conn secrets.LcppConn) (props lcppMemberProps, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lcppMemberBase(conn.URL)+"/props", nil)
	if err != nil {
		return lcppMemberProps{}, false
	}
	if conn.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	resp, err := lcppMemberClient.Do(req)
	if err != nil {
		return lcppMemberProps{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return lcppMemberProps{}, false
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&props) != nil {
		return lcppMemberProps{}, false
	}
	return props, true
}

// lcppMemberModel is one entry of GET {base}/v1/models, as answered by the member's own
// connection: the id, and the window /v1/models reports for it (data[].meta.n_ctx) — the
// field a ROUTER's /props cannot itself carry (docs/log/106 §axis 2).
type lcppMemberModel struct {
	ID   string
	NCtx int
}

// lcppMemberFetchModels reads GET {base}/v1/models directly against the member's own
// connection. ok is false on anything but a clean 200 with a readable body.
func lcppMemberFetchModels(ctx context.Context, conn secrets.LcppConn) (models []lcppMemberModel, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lcppMemberBase(conn.URL)+"/v1/models", nil)
	if err != nil {
		return nil, false
	}
	if conn.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	resp, err := lcppMemberClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				NCtx int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out) != nil {
		return nil, false
	}
	list := make([]lcppMemberModel, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		list = append(list, lcppMemberModel{ID: m.ID, NCtx: m.Meta.NCtx})
	}
	return list, true
}

// lcppMemberWindow is the member-connection counterpart of harnessEngineWindowUncached: read
// /props's own default_generation_settings.n_ctx first (a single-model llama-server — the
// common case, and what docs/log/106 §10's live LAN run actually measured), and fall back to
// GET {base}/v1/models' data[].meta.n_ctx ONLY when /props came back windowless — a router's
// own /props describes the router, not the loaded model, exactly the asymmetry
// enginePropsAugmentRouterWindow (control-plane/engine_gateway.go) reads around for the
// CP-proxied path. 0 when neither answered anything usable.
func lcppMemberWindow(ctx context.Context, conn secrets.LcppConn) int {
	if props, ok := lcppMemberProbeProps(ctx, conn); ok && props.DefaultGenerationSettings.NCtx > 0 {
		return props.DefaultGenerationSettings.NCtx
	}
	models, ok := lcppMemberFetchModels(ctx, conn)
	if !ok {
		return 0
	}
	for _, m := range models {
		if m.NCtx > 0 {
			return m.NCtx
		}
	}
	return 0
}

// lcppMemberModelsCacheTTL bounds how stale the member's own /v1/models answer may get.
// Unlike harnessEngineWindow, this enumeration (lcppModels' launch-menu path, called on every
// tools/list) is NOT already sitting behind a cache of its own — without one, a LAN box that
// merely went to sleep would pay a full round trip (bounded by lcppMemberClient's 3s timeout)
// on every single launch-menu build.
const lcppMemberModelsCacheTTL = 30 * time.Second

var lcppMemberModelsCache struct {
	mu    sync.Mutex
	at    time.Time
	value []lcppMemberModel
}

// lcppMemberFetchModelsCached is lcppMemberFetchModels behind lcppMemberModelsCacheTTL. A
// failed fetch is cached exactly like an empty answer (nil) — the same "nothing to correct"
// rule harnessEngineWindowCache already follows — so a box that is off does not cost every
// launch-menu build its own 3s timeout.
func lcppMemberFetchModelsCached(ctx context.Context, conn secrets.LcppConn) []lcppMemberModel {
	lcppMemberModelsCache.mu.Lock()
	if time.Since(lcppMemberModelsCache.at) < lcppMemberModelsCacheTTL {
		v := lcppMemberModelsCache.value
		lcppMemberModelsCache.mu.Unlock()
		return v
	}
	lcppMemberModelsCache.mu.Unlock()

	models, ok := lcppMemberFetchModels(ctx, conn)
	if !ok {
		models = nil
	}
	lcppMemberModelsCache.mu.Lock()
	lcppMemberModelsCache.value = models
	lcppMemberModelsCache.at = time.Now()
	lcppMemberModelsCache.mu.Unlock()
	return models
}

// lcppMemberCacheReset drops the short caches keyed off the member's OWN connection
// (harnessEngineWindowCache's "llm" entry, lcppMemberModelsCache) — connections.go's PUT/
// DELETE /connections/lcpp call this so a member who just changed the URL is not stuck
// looking at the PREVIOUS connection's cached window/models for the rest of the TTL.
func lcppMemberCacheReset() {
	harnessEngineWindowCache.Delete("llm")
	lcppMemberModelsCache.mu.Lock()
	lcppMemberModelsCache.at = time.Time{}
	lcppMemberModelsCache.value = nil
	lcppMemberModelsCache.mu.Unlock()
}

// --- ADR 0093 phase 1's LLM client (internal/harness) --------------------------------

// harnessEngineToken fills harness.EngineToken: an absolute base URL ending at the
// gateway's own .../v1 mount plus a bearer, exactly what engineSessionEnv/engineImageConn
// already build for opencode and imagegen. key is "llm" for the chat role; session scopes
// the token and its usage attribution the same way engineToken's other callers do.
//
// A member's own connection (docs/log/107) is checked FIRST and, when present, always wins
// over the deployment's engine — decision 1: the member's setting always wins, and clearing
// it reverts to exactly today's behavior.
func harnessEngineToken(ctx context.Context, key, session string) (harness.EngineConn, bool) {
	if key == "llm" {
		if conn, ok := harnessMemberConn(); ok {
			return harness.EngineConn{BaseURL: lcppMemberBase(conn.URL) + "/v1", Token: conn.APIKey}, true
		}
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	if base == "" {
		return harness.EngineConn{}, false
	}
	tok := engineToken(ctx, key, session)
	if tok == "" {
		return harness.EngineConn{}, false
	}
	return harness.EngineConn{BaseURL: base + "/engine/" + key + "/v1", Token: tok}, true
}

// harnessEngineWindowCache remembers harnessEngineWindow's own answer for a short time.
// Unlike syncEngineProviders (called once at boot and once per catalogue push),
// harnessEngineWindow is called from chatx's lcpp provider on every SEND — without a cache
// each turn would pay enginePropsWindow's own up-to-10s round trip a second time. 0 is
// cached exactly like a real value: a box that is asleep, or — measured live in ADR 0093
// phase 1's report — a BORROWED row whose lending deployment's Control Plane predates PR
// #761 (so /props 404s every time), are both facts that do not change turn to turn, and
// engineMeasuredWindows (engineWarmWindow's own cache) only remembers a SUCCESS, leaving
// every failing call to pay the round trip again with nothing here.
var harnessEngineWindowCache sync.Map // key -> harnessEngineWindowCacheEntry

type harnessEngineWindowCacheEntry struct {
	value int
	at    time.Time
}

// harnessEngineWindowCacheTTL bounds how stale the cached answer may get. Short enough that
// a box that just started answering /props (or a Control Plane that was just upgraded past
// #761) is picked up within a few chat turns, long enough that an ordinary back-and-forth
// conversation pays the round trip once rather than once per message.
const harnessEngineWindowCacheTTL = 5 * time.Minute

// harnessEngineWindow fills harness.EngineWindow, reusing engineWarmWindow verbatim (through
// the cache above) rather than re-reading /props a second way: the "any non-200 is nothing to
// correct, never retried" rule (decision 7, TestSyncEngineProvidersKeepsDeclaredWindowWhen…)
// has to stay exactly one implementation, or the two could drift on the very case that
// matters (an old Control Plane's /props 404 — docs/log/99 phase 1 report).
func harnessEngineWindow(ctx context.Context, key string) int {
	if v, ok := harnessEngineWindowCache.Load(key); ok {
		e := v.(harnessEngineWindowCacheEntry)
		if time.Since(e.at) < harnessEngineWindowCacheTTL {
			return e.value
		}
	}
	n := harnessEngineWindowUncached(ctx, key)
	harnessEngineWindowCache.Store(key, harnessEngineWindowCacheEntry{value: n, at: time.Now()})
	return n
}

func harnessEngineWindowUncached(ctx context.Context, key string) int {
	if key == "llm" {
		if conn, ok := harnessMemberConn(); ok {
			return lcppMemberWindow(ctx, conn)
		}
	}
	for _, e := range engineCatalogRows(ctx) {
		if e.Key != key || e.api() != engineAPIChat {
			continue
		}
		if _, nctx := engineWarmWindow(e); nctx > 0 {
			return nctx
		}
		return e.ContextTokens
	}
	return 0
}

// harnessEngineAvailable fills harness.EngineAvailable. engineCatalogRows itself already
// excludes a row with no enabled model (measured live: the Control Plane's
// /internal/engine/catalog omits an engine whose catalogue is empty entirely, not just its
// models list) — so existence in this loop already means "has at least one enabled model",
// and no separate len(Models) check is needed on top of it.
//
// A member's own connection (docs/log/107) always answers true without asking anything — a
// configured URL is by itself "this member intends to use lcpp", the same way an unset one
// falls through to whatever the catalogue says.
func harnessEngineAvailable(ctx context.Context, key string) bool {
	if key == "llm" {
		if _, ok := harnessMemberConn(); ok {
			return true
		}
	}
	for _, e := range engineCatalogRows(ctx) {
		if e.Key == key && e.api() == engineAPIChat {
			return true
		}
	}
	return false
}

// --- the usage the CP posts back -------------------------------------------------

// engineUsageReq is what control-plane/engine_usage.go sends. The CP is the only party that
// sees an engine's response, so it is the only party that can count the tokens; the ledger
// they belong in is here, next to every other feature's rows (ADR 0029).
type engineUsageReq struct {
	Feature  string `json:"feature"`  // "engine.llm"
	Provider string `json:"provider"` // "llamacpp"
	Session  string `json:"session"`
	Model    string `json:"model"`
	In       int    `json:"in"`
	Out      int    `json:"out"`
	MS       int    `json:"ms"`
	OK       bool   `json:"ok"`
	Measured string `json:"measured"`
}

// handleEngineUsage appends one engine call to the ledger (POST /engine/usage). Called by
// the CP itself, like /work-items/fetch and /notifications — never by the Console, so it
// needs no entry in the CP's agent-proxy allowlist.
func handleEngineUsage(w http.ResponseWriter, r *http.Request) {
	var req engineUsageReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	feature := strings.TrimSpace(req.Feature)
	// Only the engine features, and only ones this Agent recognises. The route is
	// authenticated, but "whatever the caller called it" would let a mislabelled row into a
	// graph whose categories are a frozen enumeration (ADR 0029 §2).
	if !strings.HasPrefix(feature, "engine.") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_feature", "feature must be engine.<key>")
		return
	}
	measured := usagex.MeasuredExact
	if req.Measured != usagex.MeasuredExact || (req.In == 0 && req.Out == 0) {
		// An engine that reported no usage and an engine that reported zero spent are
		// different facts, and merging them puts free calls into the graph.
		measured = usagex.MeasuredNone
	}
	row := usagex.Record{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Call:    chatx.RandUUID(),
		Feature: feature,
		Trigger: usagex.TriggerUser,
		// Kind is the agent kind that ran, i.e. what was driving the session — not the
		// engine. The engine is the model's provenance and rides on Model/ModelSrc.
		Kind:     engineSessionKind(req.Session),
		Model:    strings.TrimSpace(req.Model),
		ModelRaw: strings.TrimSpace(req.Model),
		ModelSrc: usagex.ModelReported,
		Ref:      strings.TrimSpace(req.Session),
		In:       req.In,
		Out:      req.Out,
		Spend:    req.In + req.Out,
		MS:       req.MS,
		OK:       req.OK,
		Measured: measured,
	}
	if err := usagex.AppendRows([]usagex.Record{row}); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "ledger_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// engineSessionKind resolves which CLI the session runs, so the row lands in the same
// dimension as that session's other consumption. An unknown session is recorded as opencode
// rather than dropped: opencode is the only kind P0 wires an engine to, and losing the row
// over a name lookup is worse than a coarse one.
func engineSessionKind(name string) string {
	if m, ok := session.ReadMeta(strings.TrimSpace(name)); ok && m.Kind != "" {
		return m.Kind
	}
	return session.KindOpencode
}
