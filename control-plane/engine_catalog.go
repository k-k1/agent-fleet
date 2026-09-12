package main

// engine_catalog.go — the engine model catalogue (ADR 0072 P0).
//
// ADR 0071 made a model a CloudFormation parameter: `LlmModelS3Key`, `ImageModelFile` and ten
// siblings flowed into the task definition's command line, the fetch sidecar and the SSM engine
// table. Swapping a checkpoint therefore meant a stack update. That is the wrong verb —
// "swap the model" is an operational act an administrator performs at night while the GPU is
// asleep, not a deployment act — and it does not fit either: 60-engines.yaml is within a
// kilobyte of CloudFormation's 51,200-byte template limit, so six parameters per model is a
// design with no room to grow.
//
// So the declaration moves. The stack keeps the VESSEL (capacity provider, service, bucket,
// task definition, ingest task); the catalogue says what goes in it. Three readers, and they
// are deliberately fed differently:
//
//   - the BOX reads an "active set" the CP publishes to SSM. It is read while the CP may not
//     even exist (60-engines is created before 30-ingress), so it cannot be an API call, and it
//     has to fit in a Standard-tier parameter — 4,096 characters, measured, not "a few KB".
//     That is why it carries S3 keys and flags and nothing else: no description, no licence.
//   - the AGENT reads /internal/engine/catalog, which is where the per-model window, the
//     description and the declared sizes live.
//   - the ADMIN PANEL reads the rows whole.
//
// Nothing here ever asks an ENGINE what it holds (ADR 0053). The engine is asleep exactly when
// somebody opens the panel.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineCatalogCacheTTL is how long one engine's rows are reused. The gateway consults the
// catalogue on every request (ADR 0072 decision 7), and a database round trip in front of every
// token is the same mistake reading the engine table per request would have been. Ten seconds
// is short enough that an administrator's toggle appears to take effect at once and long enough
// that a busy engine is not one SELECT per completion.
const engineCatalogCacheTTL = 10 * time.Second

// engineCatalog is one role's slice of engine_models, cached.
//
// A read failure keeps the previous answer rather than reporting an empty catalogue, and that
// is load-bearing rather than tidy: an empty catalogue means "do not start this engine, and
// stop it if it is up" (decision 1), so a transient database error would otherwise take a
// running GPU down and answer 503 to everyone using it.
type engineCatalog struct {
	store store.EngineModelStore
	role  string

	mu     sync.Mutex
	rows   []store.EngineModel
	at     time.Time
	loaded bool
}

func newEngineCatalog(st store.EngineModelStore, role string) *engineCatalog {
	return &engineCatalog{store: st, role: role}
}

// list returns this role's rows, refreshing at most once per TTL.
func (c *engineCatalog) list(ctx context.Context) []store.EngineModel {
	if c == nil || c.store == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.at) < engineCatalogCacheTTL {
		return c.rows
	}
	rows, err := c.store.ListEngineModels(ctx, c.role)
	if err != nil {
		log.Printf("engines: reading the %s catalogue failed: %v", c.role, err)
		return c.rows // see the type comment: never turn a DB error into "no models"
	}
	c.rows, c.at, c.loaded = rows, time.Now(), true
	return c.rows
}

// invalidate drops the cache after a write, so the panel's own re-read shows what it just did
// rather than the previous ten seconds' answer.
func (c *engineCatalog) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

// enabled is what an administrator has switched on, which is what the box syncs and what the
// gateway will route to. LoRAs are included — they are enabled the same way — so callers that
// mean "models" filter on kind themselves.
func (c *engineCatalog) enabled(ctx context.Context) []store.EngineModel {
	rows := c.list(ctx)
	out := make([]store.EngineModel, 0, len(rows))
	for _, m := range rows {
		if m.Enabled {
			out = append(out, m)
		}
	}
	return out
}

// hasModels answers the controller's "is there anything to serve" (decision 1(c)).
//
// TRUE when the catalogue cannot be read at all — a CP with no store, or a deployment older
// than this table. The false direction stops a GPU and refuses every request, so it is only
// ever returned on a definite empty answer.
func (c *engineCatalog) hasModels(ctx context.Context) bool {
	if c == nil || c.store == nil {
		return true
	}
	for _, m := range c.enabled(ctx) {
		if engineModelIsLora(m) {
			continue // a LoRA on its own is not something an engine can be started with
		}
		return true
	}
	return false
}

// engineModelKindLora is the one kind that is an accessory rather than a model. It is checked
// by name in several places, so the string is here once.
const engineModelKindLora = "lora"

func engineModelIsLora(m store.EngineModel) bool {
	return strings.EqualFold(strings.TrimSpace(m.Kind), engineModelKindLora)
}

// engineComfyFamilies is the checkpoint-family vocabulary ADR 0072 decision 2 declares, and the
// ONLY spellings the comfy provider dispatches on: it picks one of five workflow graphs by this
// string and REFUSES rather than guessing a family from the model id, because a naming
// convention eventually collides. So a catalogue row whose base_model is anything else — an
// upstream display name like "SDXL 1.0", or nothing at all — is a row ComfyUI cannot generate
// from, and the honest place to say so is where the row is written.
//
// ⚠️ Duplicated from workspace/agent/internal/imagegen/comfy_workflows.go's comfyFamily
// constants. Go cannot share it: the two are separate modules (the same situation as the shared
// contract machinery in contract_wire_test.go). engine_catalog_test.go reads that file and fails
// when the two drift, which is the only thing standing between "a sixth family was added" and
// "the Console never offers it".
var engineComfyFamilies = []string{"sdxl", "sd35", "flux1", "flux2-klein", "zimage"}

// engineComfyFileFlags is the per-file Flag vocabulary a comfy row may declare: "" for a
// single-file checkpoint, and one flag per part of a split model. Without these a catalogue row
// cannot describe FLUX.2 klein or Z-Image at all — they are a diffusion model, a text encoder
// and a VAE, three files that must each be labelled — which is why a panel offering only an S3
// key could register neither. Same duplication and same drift test as engineComfyFamilies.
//
// `--clip_g` was added after SD3.5 was first run on real hardware (ADR 0072 P2 残作業 5): with no
// way to name that file, SD3.5's TripleCLIPLoader had nothing to point at and the family could
// not generate at all. A missing flag is a family that silently does not work, which is why this
// list is served to the Console rather than hard-coded there.
var engineComfyFileFlags = []string{"", "--diffusion-model", "--clip_l", "--clip_g", "--t5xxl", "--vae"}

// engineComfyRequiredFlags is which files each family's TEMPLATE actually needs, and it is the
// difference between a row that has a family and a row that can generate.
//
// 🔴 Declaring the family clears `base_model_missing`, and until this existed nothing looked at
// whether the row held the files that family reads — so the panel went quiet about a row that
// was still refused at generation. Measured on af-sandbox (ADR 0072 P2 欠落 10): `flux1-dev` was
// a single unflagged 22.2 GiB file in `image/checkpoints/`, and the flux1 template wants a
// diffusion model, two text encoders and a VAE — no answer in the family selector could save it.
//
// Same duplication and same drift test as the two vocabularies above: the AUTHORITY is
// comfy_workflows.go's per-family guards, and engine_catalog_test.go reads them out of that
// file. Adding a family here without adding it there (or the reverse) fails that test.
var engineComfyRequiredFlags = map[string][]string{
	"sdxl":        {""},
	"sd35":        {"", "--clip_l", "--clip_g", "--t5xxl"},
	"flux1":       {"--diffusion-model", "--clip_l", "--t5xxl", "--vae"},
	"flux2-klein": {"--diffusion-model", "--clip_l", "--vae"},
	"zimage":      {"--diffusion-model", "--clip_l", "--vae"},
}

// engineMissingFileFlags answers "what would this row still be refused for", as the list of
// file roles its declared family needs and the row does not have. Empty for everything the
// question cannot be asked about: a provider that does not dispatch on a family, a LoRA, and a
// row whose family is not one of the vocabulary (`base_model_missing` is that row's answer, and
// two marks saying the same thing is one too many).
func engineMissingFileFlags(provider string, m store.EngineModel) []string {
	if engineBaseModelsFor(provider) == nil || engineModelIsLora(m) {
		return nil
	}
	want, ok := engineComfyRequiredFlags[strings.TrimSpace(m.BaseModel)]
	if !ok {
		return nil
	}
	have := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if strings.TrimSpace(f.S3Key) != "" {
			have[strings.TrimSpace(f.Flag)] = true
		}
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

// engineFileFlagsFor is the file vocabulary an engine's provider understands, or nil when a row
// is always one unlabelled file (sdcpp loads a single checkpoint with -m).
func engineFileFlagsFor(provider string) []string {
	if strings.TrimSpace(provider) == "comfy" {
		return engineComfyFileFlags
	}
	return nil
}

// engineBaseModelsFor is the vocabulary an engine's provider understands, or nil when the
// provider has no opinion. Nil is not "anything goes" by accident: sdcpp genuinely ignores
// base_model (it holds one checkpoint and never switches), so there is nothing to validate and
// nothing for a panel to offer.
func engineBaseModelsFor(provider string) []string {
	if strings.TrimSpace(provider) == "comfy" {
		return engineComfyFamilies
	}
	return nil
}

// engineBaseModelValid answers whether a row may be written for this provider. An empty family
// is refused for a provider that HAS a vocabulary, because the row would be registered,
// enabled, offered in generate_image's `model` enum — and then fail at generation with a
// message about a workflow template, minutes and a cold start later.
func engineBaseModelValid(provider, baseModel string) bool {
	vocab := engineBaseModelsFor(provider)
	if vocab == nil {
		return true
	}
	for _, f := range vocab {
		if f == strings.TrimSpace(baseModel) {
			return true
		}
	}
	return false
}

// --- the active set the box reads ------------------------------------------------

// engineActiveSet is what one engine's box is told to load. Every field name is short and
// every optional one is omitted, because the whole document has to fit in an SSM Standard-tier
// parameter: 4,096 CHARACTERS, measured (a 4,200-character PutParameter is refused with
// `ValidationException`). Advanced tier is 8 KB at $0.05/month and is a decision to take when
// a real deployment needs it, not a default to spend on a maybe.
//
// There is no description, licence or vram figure here on purpose. Those belong to the panel
// and to the Agent's catalogue, and putting them in this document is how it would quietly stop
// fitting.
type engineActiveSet struct {
	// V is the shape's version. The sidecar is baked into a CloudFormation template and a box
	// can be running a template older than the CP that writes this.
	V int `json:"v"`
	// Key is the engine key, echoed so a parameter read from the wrong path is obvious in a log.
	Key string `json:"key"`
	// Start names the model the engine is STARTED with: the image role's selected checkpoint,
	// or the llm role's default. One field for both because the sidecar has no role-specific
	// branch — that is what "one sidecar, not one per role" means (decision 1(a)).
	Start  string              `json:"start,omitempty"`
	Models []engineActiveModel `json:"models,omitempty"`
	// Loras are BARE S3 KEYS, not entries. A LoRA is never started with, carries no flags and
	// has no window, and the engines find them by scanning a directory (`--lora-model-dir`,
	// ComfyUI's `models/loras`) — so the box needs the files and nothing else. The name, the
	// description and the base model an agent chooses by stay in the catalogue, where the
	// character budget is not 4,096. Measured: as entries, 20 LoRAs cost 1,840 characters of
	// the document against 1,000 as keys.
	Loras []string `json:"loras,omitempty"`
}

type engineActiveModel struct {
	ID string `json:"id"`
	// Files is a mixed list on purpose: a plain STRING is a key whose file goes to the
	// engine's own `-m`, and the object form is one part of a split model with the literal
	// flag it belongs to. The overwhelming case is one flagless file, and wrapping that in
	// `{"k":…}` costs seven characters times every model in the deployment.
	Files []any    `json:"f,omitempty"`
	Args  []string `json:"a,omitempty"`
	// Ctx is the window this model is started with (llama-server's -c). Per MODEL rather than
	// per engine, which is what ADR 0072 decision 3 turns into a router flag in P1.
	Ctx int `json:"c,omitempty"`
	// Lo are the LoRA adapters PINNED to this model (ADR 0072 decision 5, the llm half): each
	// entry is `<s3 key>:<scale>`, which is llama.cpp's own `--lora-scaled FNAME:SCALE` spelling
	// so the sidecar joins them with commas and writes ONE preset key.
	//
	// Pinned rather than chosen per request: to the member this is a model that happens to
	// include a fine-tune, and opencode sees an ordinary model id. Choosing per request is the
	// virtual-model-id half of the same decision, and it is deliberately later (P5) because it
	// makes the gateway rewrite a request body for the first time.
	//
	// The scale is always written, `1` included: `--lora-scaled x:1` is exactly `--lora x`, and
	// one spelling is one code path in the jq that builds the preset.
	Lo []string `json:"lo,omitempty"`
}

// engineActiveFile is one part of a SPLIT model. There is no local path: the box mirrors the
// bucket, so `image/checkpoints/x.safetensors` lands at
// `/models/image/checkpoints/x.safetensors`. That halves the document AND leaves a tree
// ComfyUI reads unchanged (ADR 0071 decision 6).
type engineActiveFile struct {
	// Flag is the literal engine flag this file is passed to (`--vae`, `--t5xxl`).
	Flag string `json:"g"`
	Key  string `json:"k"`
}

// engineActiveSetMaxChars is SSM's Standard-tier limit. Measured against the real API on
// 2026-09-08: 4,200 characters came back `ValidationException: Standard tier parameters
// support a maximum parameter value of 4096 characters`.
const engineActiveSetMaxChars = 4096

// buildEngineActiveSet turns one role's enabled rows into the document the box reads.
func buildEngineActiveSet(key string, rows []store.EngineModel) engineActiveSet {
	set := engineActiveSet{V: 1, Key: key}
	for _, m := range rows {
		if !m.Enabled {
			continue
		}
		if engineModelIsLora(m) {
			for _, f := range m.Files {
				if k := strings.TrimSpace(f.S3Key); k != "" {
					set.Loras = append(set.Loras, k)
				}
			}
			continue
		}
		e := engineActiveModel{ID: m.ID, Args: m.Args, Ctx: m.ContextTokens, Lo: engineLorasPinnedTo(m.ID, rows)}
		for _, f := range m.Files {
			k := strings.TrimSpace(f.S3Key)
			if k == "" {
				continue
			}
			if f.Flag == "" {
				e.Files = append(e.Files, k)
				continue
			}
			e.Files = append(e.Files, engineActiveFile{Flag: f.Flag, Key: k})
		}
		set.Models = append(set.Models, e)
		// Selected (image) and Default (llm) are the same question asked of two roles, and the
		// store keeps each exclusive within its role, so the last writer here cannot disagree
		// with itself.
		if m.Selected || m.Default {
			set.Start = m.ID
		}
	}
	// Something has to be started with. Falling back to the first enabled model rather than
	// leaving it empty matters for the seeded case and for an administrator who enabled a
	// checkpoint but never pressed "select": an engine with models and no start flag would
	// come up as the `sleep infinity` placeholder, which reads exactly like a broken deploy.
	if set.Start == "" && len(set.Models) > 0 {
		set.Start = set.Models[0].ID
	}
	return set
}

// engineLoraScaleArg is how a LoRA row declares the strength it is pinned at. It rides in the
// row's `args` rather than in a column of its own because a weight is meaningful for exactly one
// kind of row, and `args` is already the place a row says something only its engine understands.
// Absent, unreadable or out of range means 1 — the strength llama.cpp's own `--lora` applies.
const engineLoraScaleArg = "--scale"

// engineLorasPinnedTo is the adapters a model carries, as `<key>:<scale>` entries.
//
// A LoRA names its base by ID in `base_model` — the llm role's vocabulary for that column, where
// the image role puts a ComfyUI family (decision 2). Both are "what this adapter belongs to", and
// neither is ever guessed from a name.
//
// A LoRA whose base is disabled or absent is pinned to NOTHING rather than to something else.
// That case is not silent: engineAdminAPI.row marks the row `lora_base_missing`, because an
// adapter that quietly does nothing looks exactly like one that is working.
func engineLorasPinnedTo(base string, rows []store.EngineModel) []string {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil
	}
	var out []string
	for _, l := range rows {
		if !l.Enabled || !engineModelIsLora(l) || strings.TrimSpace(l.BaseModel) != base {
			continue
		}
		scale := engineLoraScale(l)
		for _, f := range l.Files {
			if k := strings.TrimSpace(f.S3Key); k != "" {
				out = append(out, k+":"+scale)
			}
		}
	}
	return out
}

// engineLoraBasePresent answers whether this adapter has something to be pinned to: an ENABLED
// model row (not another LoRA) whose id is its `base_model`.
func engineLoraBasePresent(lora store.EngineModel, rows []store.EngineModel) bool {
	base := strings.TrimSpace(lora.BaseModel)
	if base == "" {
		return false
	}
	for _, m := range rows {
		if m.Enabled && !engineModelIsLora(m) && strings.TrimSpace(m.ID) == base {
			return true
		}
	}
	return false
}

// engineLoraScale reads `--scale <v>` out of a row's args, as the string the preset carries.
// Rendered from the parsed float rather than echoed, so nothing an operator typed reaches a
// command line unchecked — `--lora-scaled` splits on `:` and `,`, and a value holding either
// would silently move the boundary between two adapters.
func engineLoraScale(m store.EngineModel) string {
	for i, a := range m.Args {
		if strings.TrimSpace(a) != engineLoraScaleArg || i+1 >= len(m.Args) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(m.Args[i+1]), 64)
		if err != nil || v < 0 || v > 2 {
			break // the same range the image side offers, and the same answer to nonsense: the default
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return "1"
}

// engineActiveSetJSON renders the document and refuses one that cannot be read back.
//
// The size check is here rather than at the call site because EVERY writer has to make it: an
// active set that is one character too long is rejected by SSM, and the failure surfaces as an
// engine that keeps loading yesterday's model — the parameter simply does not change.
func engineActiveSetJSON(set engineActiveSet) (string, error) {
	// The box builds a shell command line out of these keys and relies on word splitting to
	// turn `/models/cmdline` into arguments, so a key with whitespace in it becomes two
	// arguments and the engine starts with a file name it cannot open. S3 permits the
	// character; this pipeline does not, and the honest place to say so is here rather than in
	// a start failure nobody can read.
	for _, m := range set.Models {
		for _, f := range m.Files {
			if err := engineActiveKeyOK(engineActiveFileKey(f)); err != nil {
				return "", fmt.Errorf("model %s: %w", m.ID, err)
			}
		}
	}
	for _, k := range set.Loras {
		if err := engineActiveKeyOK(k); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(set)
	if err != nil {
		return "", err
	}
	if len(b) > engineActiveSetMaxChars {
		return "", fmt.Errorf("the active set for %s is %d characters, over SSM's Standard-tier limit of %d: "+
			"disable some models, or move the parameter to Advanced tier",
			set.Key, len(b), engineActiveSetMaxChars)
	}
	return string(b), nil
}

// engineActiveFileKey reads the key out of either form of a file entry.
func engineActiveFileKey(f any) string {
	switch v := f.(type) {
	case string:
		return v
	case engineActiveFile:
		return v.Key
	}
	return ""
}

func engineActiveKeyOK(key string) error {
	if key == "" || strings.ContainsAny(key, " \t\n\r") {
		return fmt.Errorf("the S3 key %q has whitespace in it, which the engine's command line cannot carry", key)
	}
	// `,` and `:` are llama.cpp's own separators in `--lora-scaled FNAME:SCALE,...`, which is how
	// a pinned adapter reaches the preset (decision 5). A key holding either would not fail — it
	// would move the boundary between two adapters, and the engine would load a path nobody
	// named. Refused for every key rather than for LoRAs alone: one rule is one thing to know,
	// and no key this deployment writes has ever wanted them.
	if strings.ContainsAny(key, ",:") {
		return fmt.Errorf("the S3 key %q holds a ',' or ':', which llama.cpp reads as a separator between LoRA adapters", key)
	}
	return nil
}

// engineActiveParamName is where one engine's active set lives, derived from the engine table's
// own parameter name so a deployment that moved that prefix moves both together. The CP task
// role's SSM write is scoped to /af-ws/*, which is the whole reason the base has to stay there.
func engineActiveParamName(base, key string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + key + "/active"
}

// engineSSMWriteAPI is the narrow write port, so a test can capture what was published.
type engineSSMWriteAPI interface {
	PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
}

// publishActiveSet writes one engine's active set to SSM.
//
// It is called at CP start and after every catalogue change, and it is deliberately NOT called
// from the request path: the box reads this once, when it starts, and republishing it per
// request would be an SSM write in front of every completion.
func (e *engineRuntimeState) publishActiveSet(ctx context.Context) error {
	if e.ssm == nil || e.activeParam == "" {
		return nil // an inline dev table, or a CP with no AWS: nothing reads it
	}
	value, err := engineActiveSetJSON(buildEngineActiveSet(e.def.Key, e.catalog.list(ctx)))
	if err != nil {
		return err
	}
	_, err = e.ssm.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(e.activeParam),
		Type:      "String",
		Value:     aws.String(value),
		Overwrite: aws.Bool(true),
		// Deliberately no Tier: Standard is the default and asking for Advanced would start
		// charging $0.05/month per engine for a document that fits.
	})
	if err != nil {
		return fmt.Errorf("publishing the active set to %s: %w", e.activeParam, err)
	}
	log.Printf("engines: %s active set published to %s (%d bytes)", e.def.Key, e.activeParam, len(value))
	return nil
}

// --- the shapes the Agent and the panel read --------------------------------------

// engineCatalogModelRow is one model as /internal/engine/catalog reports it. The Agent turns
// these into opencode's per-model `limit` and into generate_image's own model list, so the
// window is per MODEL here — the correction ADR 0071 had to make after P1 ("two models with
// different windows are two engines") is exactly what the catalogue removes.
//
// The window is omitted rather than zeroed when it was never declared. A zero reaches opencode
// as a context of 0, which switches auto-compaction off — the state the field exists to fix.
// warm is the id of the model the Control Plane last saw THIS engine actually answer with
// (ADR 0072 decision 7's warm_model) — "" when nothing is known warm, in which case no row ever
// gets the flag. It rides on this function rather than being read off m itself because warmth is
// an in-memory, per-CP-process fact (engineServed), never a stored column: a served model column
// would say something is warm when it might not even be the CP process that watched it happen.
func engineCatalogModelRow(m store.EngineModel, warm string) map[string]any {
	row := map[string]any{"id": m.ID}
	if m.ContextTokens > 0 {
		row["context_tokens"] = m.ContextTokens
		if m.MaxOutputTokens > 0 {
			row["max_output_tokens"] = m.MaxOutputTokens
		}
	}
	if m.Description != "" {
		row["description"] = m.Description
	}
	if len(m.Sizes) > 0 {
		row["sizes"] = m.Sizes
	}
	if m.BaseModel != "" {
		row["base_model"] = m.BaseModel
	}
	// What this model asks to be run at, when its row says (store.EngineParams). The provider
	// reads it field by field over its family's own recipe, so a row that declares two numbers
	// changes two numbers — which is why it rides as the whole object and not as a flattened
	// set of scalars that cannot tell "declared 0" from "did not say".
	if m.Params != nil {
		row["params"] = m.Params
	}
	if m.Selected {
		row["selected"] = true
	}
	if m.Default {
		row["default"] = true
	}
	if warm != "" && m.ID == warm {
		row["warm"] = true
	}
	// Files (ADR 0072 P2): the comfy provider is the first reader, to fill in a workflow
	// template's loader nodes. Kept in sd.cpp's own flag spelling (EngineModelFile's own
	// comment) rather than translated into a second vocabulary for the same fact.
	if len(m.Files) > 0 {
		files := make([]map[string]any, 0, len(m.Files))
		for _, f := range m.Files {
			files = append(files, map[string]any{"flag": f.Flag, "s3_key": f.S3Key})
		}
		row["files"] = files
	}
	return row
}

// engineSyncMBps is what S3 to the box's EBS volume actually ran at, the slow end of the
// measured band (104-147 MB/s over four cold starts, ADR 0071 measurement 8 and ADR 0072 P0
// measurement 5). It turns a declared file size into the seconds enabling a model adds to the
// next cold start — an ESTIMATE, and the panel says so: the fast end is 40% quicker and a box
// that already holds the file pays nothing.
const engineSyncMBps = 104

// engineSyncSecs is how long this model's files take to reach the box, or 0 when nobody
// declared their size. Rounded UP: the number exists to set an expectation about a wait, and a
// 40-second sync reported as "+0 s" is worse than saying nothing.
func engineSyncSecs(m store.EngineModel) int {
	var total int64
	for _, f := range m.Files {
		total += f.Bytes
	}
	if total <= 0 {
		return 0
	}
	per := int64(engineSyncMBps) * 1000 * 1000
	return int((total + per - 1) / per)
}

// engineAdminModelRow is one model as the admin panel reads it: everything the catalogue holds
// except the S3 keys' bulk, plus the licence fields, which are the whole point of keeping two
// of them (Hugging Face reports `other` for both non-commercial models in ADR 0072's table).
func engineAdminModelRow(m store.EngineModel) map[string]any {
	row := map[string]any{
		"id":       m.ID,
		"kind":     m.Kind,
		"enabled":  m.Enabled,
		"selected": m.Selected,
		"default":  m.Default,
	}
	if m.Description != "" {
		row["description"] = m.Description
	}
	if m.ContextTokens > 0 {
		row["context_tokens"] = m.ContextTokens
		row["max_output_tokens"] = m.MaxOutputTokens
	}
	if m.VramMiB > 0 {
		row["vram_mib"] = m.VramMiB
	}
	// What this model would want on the card, and how well that is known (ADR 0074 decision 6).
	// Computed here rather than in the panel because the file sizes it is derived from are not
	// on the wire — and because "unknown" has to be a value the client receives, not the absence
	// of one, which is what would let it be drawn as a comfortable 0.
	if need, source := engineModelVramNeed(m); source != engineVramUnknown {
		row["vram_need_mib"] = need
		row["vram_need_source"] = source
	} else {
		row["vram_need_source"] = engineVramUnknown
	}
	if m.License != "" {
		row["license"] = m.License
	}
	if m.LicenseName != "" {
		row["license_name"] = m.LicenseName
	}
	if m.LicenseURL != "" {
		row["license_url"] = m.LicenseURL
	}
	if m.BaseModel != "" {
		row["base_model"] = m.BaseModel
	}
	// The generation defaults this row declares (store.EngineParams). Absent when it declares
	// none, which is what the panel draws as "the family's own recipe" — an object of zeros
	// would read as "this model runs at 0 steps".
	if m.Params != nil {
		row["params"] = m.Params
	}
	// Which vendor's model of that name this is. Absent for a seeded row, which came from the
	// stack rather than from anywhere with a URL — and absent rather than "unknown", so the
	// panel omits the line instead of asserting a provenance nobody recorded.
	if m.Source != "" {
		row["source"] = m.Source
	}
	if m.Precision != "" {
		row["precision"] = m.Precision
	}
	if len(m.Sizes) > 0 {
		row["sizes"] = m.Sizes
	}
	if files := engineModelFileNames(m); len(files) > 0 {
		row["files"] = files
	}
	// 🔴 And the row as it was DECLARED — the S3 keys, the flags and the sizes, in the shape
	// `POST …/models` reads back. `files` above is base names for a person to read, which is
	// not enough to rebuild anything: since P6 the catalogue is the only declaration there is,
	// so a forgotten row was recoverable only by whoever happened to have kept a copy. One
	// file could be retyped from a note (measured on the dev deployment, ADR 0072 P6 R2); a
	// FLUX.1 row is four keys and four flags and could not.
	//
	// Super-admin only, which is what this whole map is (GET /api/admin/engines is
	// withSuperAdmin) — the Agent's catalogue is built by engineCatalogModelRow and carries
	// none of this.
	if rows := engineModelFileRows(m); len(rows) > 0 {
		row["file_rows"] = rows
	}
	// The per-model flags, for the same reason: they are part of the declaration and nothing
	// else on the wire carries them.
	if len(m.Args) > 0 {
		row["args"] = m.Args
	}
	if s := engineSyncSecs(m); s > 0 {
		row["sync_secs"] = s
	}
	// Who accepted the licence, and whether this deployment may charge for what the model
	// makes (ADR 0072 decision 10). Both are absent rather than empty when nobody recorded
	// them — a row staged before phase P4, or registered by hand.
	if m.LicenseAcceptedBy != "" {
		row["license_accepted_by"] = m.LicenseAcceptedBy
		row["license_accepted_at"] = m.LicenseAcceptedAt
		// Under whose grant it was accepted, and to what (ADR 0072 open question 11). Both
		// stay ABSENT rather than empty: no tenant means a super_admin accepted for the
		// deployment, which the panel draws differently from "a tenant did", and a blank
		// licence means nobody wrote one down rather than "no terms".
		if m.LicenseAcceptedTenant != "" {
			row["license_accepted_tenant"] = m.LicenseAcceptedTenant
		}
		if m.LicenseAcceptedLicense != "" {
			row["license_accepted_license"] = m.LicenseAcceptedLicense
		}
	}
	if m.CommercialUse != "" {
		row["commercial_use"] = m.CommercialUse
	}
	return row
}

// engineModelFileRows is the file list a MACHINE reads: the whole declaration, keyed exactly
// as `POST …/models` takes it, so the answer to "what was this row" can be posted straight
// back. `s3Key` rather than `s3_key` for that reason alone — it is the spelling the register
// route already reads, and a round trip that needed a rename would not be one.
//
// Empty fields are omitted: a flagless file is the whole checkpoint, and a size nobody
// declared must stay undeclared rather than come back as a measured 0.
func engineModelFileRows(m store.EngineModel) []map[string]any {
	out := make([]map[string]any, 0, len(m.Files))
	for _, f := range m.Files {
		k := strings.TrimSpace(f.S3Key)
		if k == "" {
			continue
		}
		row := map[string]any{"s3Key": k}
		if flag := strings.TrimSpace(f.Flag); flag != "" {
			row["flag"] = flag
		}
		if f.Bytes > 0 {
			row["bytes"] = f.Bytes
		}
		out = append(out, row)
	}
	return out
}

// engineModelFileNames is the file list a person reads — base names, not keys. The full key is
// in the bucket, in the active set and in file_rows above; a row's meta line is not where
// somebody reconstructs a path.
func engineModelFileNames(m store.EngineModel) []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		if k := strings.TrimSpace(f.S3Key); k != "" {
			out = append(out, path.Base(k))
		}
	}
	return out
}
