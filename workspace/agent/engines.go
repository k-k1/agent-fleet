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
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
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
}

// The API families the CP's catalogue reports. Chat engines become opencode providers; images
// engines become the imagegen `sdcpp` provider. The key is NOT what decides this — an engine's
// role is declared by the stack (ADR 0071 decision 8).
const (
	engineAPIChat   = "chat"
	engineAPIImages = "images"
)

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
}

// engineCatalogModel is one model as the catalogue describes it.
type engineCatalogModel struct {
	ID              string   `json:"id"`
	ContextTokens   int      `json:"context_tokens"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Description     string   `json:"description"`
	Sizes           []string `json:"sizes"`
	BaseModel       string   `json:"base_model"`
	Selected        bool     `json:"selected"`
	Default         bool     `json:"default"`
	// Files are the on-disk basenames ADR 0072 decision 2 declares, only sent for the image
	// role's non-LoRA models — the comfy provider (ADR 0072 P2) is the first reader; sdcpp
	// never asked because it holds one checkpoint and never chooses which file to load.
	Files []engineCatalogFile `json:"files"`
	// Warm is whether the Control Plane last saw the engine actually answer with THIS model
	// (ADR 0072 decision 7's warm_model). At most one row per engine has it true.
	Warm bool `json:"warm"`
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
	if len(rows) == 0 {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	providers := make([]opencode.EngineProvider, 0, len(rows))
	for _, e := range rows {
		// CHAT engines only. An image engine declared here would put `sdcpp/sdxl-base-1.0`
		// in the launch menu as something to hold a conversation with, and picking it would
		// send a chat completion to an endpoint that has never heard of one.
		if e.api() != engineAPIChat {
			continue
		}
		providers = append(providers, opencode.EngineProvider{
			Key: e.Key, Provider: e.Provider, BaseURL: base + e.BaseURL, Models: e.Models,
			ContextTokens: e.ContextTokens, MaxOutputTokens: e.MaxOutputTokens,
			Windows: engineModelWindows(e),
		})
	}
	changed, err := opencode.WriteEngineProviders(providers)
	if err != nil {
		log.Printf("engines: writing the opencode provider failed: %v", err)
		return
	}
	if changed {
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			names = append(names, p.Provider+" ("+strings.Join(p.Models, ",")+")")
		}
		log.Printf("engines: opencode provider written: %s", strings.Join(names, "; "))
		// A serve daemon that was already up read its config, and its `{env:…}`, at start. At
		// boot there is none and this costs nothing; it matters for a workspace whose sessions
		// resumed before this call finished, and for a catalogue that changes later.
		opencode.ApplyEngineChange(strings.Join(names, "; "))
	}
}

// engineModelWindows is the per-model context/output declaration, or nil when the Control
// Plane sent none — in which case opencode gets the engine-wide pair, exactly as before.
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

// engineImageConn tells internal/imagegen how to reach the fleet's own image engine, or says
// there is none. Called from the provider's Ready() (the tools/list path) and from its
// Generate, so both sides of "is it offered" and "can it run" agree by construction.
//
// The token is WORKSPACE-scoped, not per session, and that is a different trade from
// opencode's. The engine credential opencode uses ends up in a config the model can read
// through `{env:…}`, so a narrow scope limits what a leak costs; this one never leaves the
// Agent's own process. What the CP does with the session claim is label a usage row, and for
// images the row is written here instead (feature tool.imagegen, with the session as its ref),
// so a session-scoped token would buy nothing and cost one credential per session.
func engineImageConn(ctx context.Context, provider string) (imagegen.EngineConn, bool) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AF_CP_BASE_URL")), "/")
	if base == "" {
		return imagegen.EngineConn{}, false
	}
	for _, e := range engineCatalogRows(ctx) {
		if e.api() != engineAPIImages || e.Provider != provider {
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
			Descriptions: engineImageDescriptions(e),
			Loras:        engineImageLoras(e),
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
		out = append(out, imagegen.EngineLora{
			ID: m.ID, File: name, Description: m.Description, BaseModel: m.BaseModel,
		})
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
