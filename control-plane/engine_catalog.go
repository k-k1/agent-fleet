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
		e := engineActiveModel{ID: m.ID, Args: m.Args, Ctx: m.ContextTokens}
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

// --- the seed (decision 7) --------------------------------------------------------

// seedEngineCatalog creates the catalogue row a deployment upgrading from ADR 0071 already has
// in its stack, so that CP comes up serving exactly what it served before.
//
// It runs only when the role's catalogue is EMPTY. Anything else would overwrite an
// administrator's own choices with a CloudFormation parameter on every restart, which is
// precisely the direction this ADR is moving away from.
//
// ⚠️ The seed and the S3 layout move go together (decision 2(f)). The row points at
// `modelS3Key` verbatim, so a deployment that moves `image/x.safetensors` under
// `image/checkpoints/` without updating that parameter seeds a row whose file is not there,
// and the first start fails in the fetch sidecar rather than at deploy time.
func seedEngineCatalog(ctx context.Context, st store.EngineModelStore, d engineDef) error {
	if st == nil || strings.TrimSpace(d.ModelS3Key) == "" || len(d.Models) == 0 {
		return nil
	}
	rows, err := st.ListEngineModels(ctx, d.Key)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return nil
	}
	id := strings.TrimSpace(d.Models[0])
	if id == "" {
		return nil
	}
	m := store.EngineModel{
		Role: d.Key, ID: id,
		Kind:  engineSeedKind(d),
		Files: []store.EngineModelFile{{S3Key: strings.TrimSpace(d.ModelS3Key)}},
		// Enabled AND started with: this is the model the deployment is already running, so
		// seeding it switched off would turn an upgrade into an outage.
		Enabled: true, Selected: d.api() == engineAPIImages, Default: d.api() == engineAPIChat,
		ContextTokens: d.ContextTokens, MaxOutputTokens: d.MaxOutputTokens,
		Description: "seeded from the 60-engines stack (ADR 0072 decision 7)",
	}
	if err := st.PutEngineModel(ctx, m); err != nil {
		return err
	}
	log.Printf("engines: %s catalogue seeded from the stack: %s -> %s", d.Key, id, d.ModelS3Key)
	return nil
}

// engineSeedKind names what the stack staged, from the API family rather than from the file
// name: `chat` is a GGUF llama.cpp loads, `images` is a checkpoint sd-server loads.
func engineSeedKind(d engineDef) string {
	if d.api() == engineAPIImages {
		return "checkpoint"
	}
	return "gguf"
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
	if s := engineSyncSecs(m); s > 0 {
		row["sync_secs"] = s
	}
	// Who accepted the licence, and whether this deployment may charge for what the model
	// makes (ADR 0072 decision 10). Both are absent rather than empty when nobody recorded
	// them — a row staged before phase P4, or registered by hand.
	if m.LicenseAcceptedBy != "" {
		row["license_accepted_by"] = m.LicenseAcceptedBy
		row["license_accepted_at"] = m.LicenseAcceptedAt
	}
	if m.CommercialUse != "" {
		row["commercial_use"] = m.CommercialUse
	}
	return row
}

// engineModelFileNames is the file list a person reads — base names, not keys. The full key is
// in the bucket and in the active set; a panel row is not where somebody reconstructs a path.
func engineModelFileNames(m store.EngineModel) []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		if k := strings.TrimSpace(f.S3Key); k != "" {
			out = append(out, path.Base(k))
		}
	}
	return out
}
