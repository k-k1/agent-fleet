package store

// store_engine_models.go — the engine model catalogue (ADR 0072 decision 2).
//
// Dialect-neutral like the rest of the store: `?` placeholders the wrapper rebinds, and
// `ON CONFLICT … DO UPDATE`, which SQLite and Postgres spell the same way.
//
// The three list-shaped columns (files, args, sizes) are JSON text rather than child tables.
// They are read and written whole, always by the same row's owner, and nothing joins or filters
// on their contents — a child table would buy referential integrity for a relation nobody
// queries and cost three more migrations to keep in step.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// EngineModel is one entry of the engine model catalogue (ADR 0072 decision 2): what a
// self-hosted engine may load, declared by an administrator rather than by a CloudFormation
// parameter. Two layers make up the truth — S3 says what EXISTS, this says what is OFFERED —
// and neither of them is the engine, which is asleep whenever the question is asked (ADR 0053).
//
// Role is the ENGINE KEY, not a separate taxonomy: the row belongs to `llm` or `image` the
// same way the SSM engine table's rows do, so `PUT /api/admin/engines/{key}/models/{id}`
// carries both halves of the primary key.
type EngineModel struct {
	Role, ID, Kind string
	// Files is one entry for a single-file model and several for a split one (FLUX's
	// diffusion_model + clip_l + t5xxl + vae). It is what the fetch sidecar syncs.
	Files []EngineModelFile
	// Enabled = synced onto the box and offered. Selected is the image role's ONE checkpoint
	// (sd-server holds one and cannot switch at request time); Default is the llm role's
	// answer to a request that named no model. Both are exclusive within a role.
	Enabled, Selected, Default bool
	Args                       []string
	// ContextTokens/MaxOutputTokens move here from the engine because with a router the window
	// is per MODEL (ADR 0072 decision 3). Zero means undeclared, and undeclared must stay
	// unwritten — opencode reads a limit of 0 as "no auto-compaction".
	ContextTokens, MaxOutputTokens int
	// The attention geometry, read once from the model's GGUF header at registration and kept
	// so that the VRAM answer can include the KV cache (ADR 0074 open question 7). Zero in ANY
	// of the four means the header was not read — a partial geometry is not a smaller estimate,
	// it is a wrong one, so the row stays at its floor.
	//
	// Only the llm role fills these: a GGUF is where they are written down, and an image
	// checkpoint's memory is dominated by the compute buffers instead (ADR 0074's first
	// measurement).
	KVLayers, KVHeadsKV, KVKeyLen, KVValueLen int
	// How many of KVLayers actually cache: KVNextN is <arch>.nextn_predict_layers (blocks
	// inside block_count that llama.cpp never runs) and KVFullAttnInterval is
	// <arch>.full_attention_interval (only every Nth layer is full attention — the rest are
	// recurrent, with a state that does not grow with the window).
	//
	// Unlike the four above, a zero here is SAFE: it means the architecture has no such field,
	// or the row was written before they were read, and both reduce to "every layer caches".
	// See engineKVGeometry.cacheLayers for the measurement that made them necessary.
	KVNextN, KVFullAttnInterval int
	// The per-layer geometry, folded into what the cache arithmetic needs: KVFullWidth and
	// KVSWAWidth are Σ n_head_kv(il) × (key_length(il) + value_length(il)) over the layers that
	// cache every token and over the sliding-window layers, and KVSlidingWindow is
	// <arch>.attention.sliding_window. See engineKVGeometry.FullWidth for how they are read.
	//
	// When either width is non-zero it SUPERSEDES the formula over the six columns above, which
	// can only describe a model whose layers all look alike. All three zero means the row was
	// read before these existed — the six-column formula still answers, and healGeometry reads
	// the header again.
	KVFullWidth, KVSWAWidth, KVSlidingWindow int
	// ContextCeiling is `<arch>.context_length`, the largest window the model was TRAINED for.
	// 🔴 A ceiling, not a setting — the 27B here publishes 262144 and is run at 32768. It is
	// stored so a registered row can be re-fitted against its own limit, which the panel cannot
	// do from the geometry alone: the attention shape says what a token costs, not how many of
	// them the weights can attend over. 0 = not read.
	ContextCeiling int
	// Sizes replaces sdcppSizes()'s guess from the model id with a declaration.
	Sizes []string
	// Params are the generation defaults this row asks for — see EngineParams. Nil for a row
	// that declares none, which is the family template's own recipe and stays the normal case.
	Params      *EngineParams
	Description string
	VramMiB     int
	// License is HF's `cardData.license` verbatim and LicenseName is where the terms really
	// are when that says "other" — which is the case for both non-commercial models in the
	// ADR's table. Keeping only the first shows them as "other" and nothing else.
	License, LicenseName, LicenseURL string
	Precision, BaseModel             string
	// NegativePrompt is what this checkpoint should keep OUT of every picture, as its publisher
	// recommends it. A DEFAULT for the model, not a policy: a request's own negative prompt is
	// added to it, and the engine-wide exclusion list an administrator sets is added to both.
	// Empty means undeclared, which the Agent answers with its own measured default rather than
	// with an empty negative prompt.
	NegativePrompt string
	// TrainedWords are the words an ADAPTER answers to, as its publisher records them
	// (Civitai's `trainedWords`). Empty on a checkpoint, which has no triggers at all.
	//
	// A LoRA loaded without its trigger changes nothing visible, which reads as a broken
	// ingest — so the words are part of what the catalogue has to carry, not a detail of the
	// wizard that found them (ADR 0081 decision 5).
	TrainedWords []string
	// Who accepted the licence and when (ADR 0072 decision 10). A record of a HUMAN act: a
	// gated repository distributes only to accounts that accepted its terms, and in a
	// multi-tenant deployment the operator accepts on behalf of every member — so the
	// question an audit asks is who did that, which the model card cannot answer.
	LicenseAcceptedBy, LicenseAcceptedAt string
	// The other two halves of the tuple ADR 0072 open question 11 asks for — the acceptance
	// is (tenant_id, member_id, accepted_at, license) and the two above carry the middle pair.
	//
	// LicenseAcceptedTenant is EMPTY for a super_admin: the operator acts for the deployment
	// and has no tenant to be acting for. A tenant id means a tenant_admin accepted under the
	// grant their operator gave that tenant (tenantLimits.AllowEngineIngest).
	//
	// LicenseAcceptedLicense is the licence as it read at that moment, held apart from
	// License/LicenseName next door because those describe the MODEL and are corrected when
	// the model card is, while this is evidence about a past act and must not move with it.
	LicenseAcceptedTenant, LicenseAcceptedLicense string
	// CommercialUse is "yes" | "no" | "unknown", resolved from the licence at ingest. It says
	// what may be restricted rather than naming the act: which of "running the model" and
	// "selling what it generates" a given non-commercial licence forbids differs between them,
	// and the panel points at the licence rather than deciding.
	CommercialUse string
	// Source is where the bytes came from — `hf:<repo>/<file>`, `civitai:<id>` or a URL.
	//
	// A snapshot like the licence beside it, and it answers the question the id cannot. An id
	// only has to be unique within one role in one deployment, so it stays short and readable
	// (it is what a member sees in the launch menu); two vendors publishing a model of the same
	// name is a collision the ingest REFUSES rather than one a longer id prevents. But refusing
	// the second is not the same as recording which vendor the first came from, and
	// engine_ingest_jobs.source keeps that only for as long as the job row lives.
	//
	// Empty for a seeded row, which comes from the stack and not from anywhere with a URL.
	Source string
	// What the publisher CALLS this, and which version of it was taken in (ADR 0088). The id
	// next door is derived from the file name because it is the key the launch menu, the active
	// set and the S3 layout are written in — so it is not a name anybody chose, and a catalogue
	// drawn from ids alone (`abyssorangemix2_hard_8832`) says nothing about which model is
	// which.
	//
	// Both are a SNAPSHOT, like the licence: the row records what the model page said when it
	// was read, and the metadata route re-reads it on request. Empty means nobody recorded one —
	// a seeded row, a row staged by hand, and every row taken in before this existed.
	DisplayName, VersionName string
	// One example image the publisher published, at the two sizes the panel draws (the card and
	// the lightbox). URLs into the publisher's own CDN and never bytes this deployment holds:
	// nothing is mirrored into the bucket, so an image the publisher deletes is an empty box and
	// not a third kind of object that purge, the ledger and the object routes would have to
	// learn about.
	PreviewURL, ThumbURL string
	CreatedAt, UpdatedAt string
}

// EngineModelDisplay is the four fields above as one value, because they are one snapshot of
// one upstream document and every writer of them writes all four (SetEngineModelDisplay).
type EngineModelDisplay struct {
	DisplayName, VersionName string
	PreviewURL, ThumbURL     string
}

// EngineParams are the generation defaults declared for one catalogue row.
//
// Until this existed, sampler, steps, cfg and scheduler were the FAMILY's fixed recipe inside
// the five workflow templates (comfy_workflows.go), and a checkpoint whose author publishes
// different numbers had nowhere to say so. Those numbers are real — the whole of Civitai is
// people writing "Steps: 30, CFG 4, DPM++ 2M" under their uploads — and the catalogue row is
// where a deployment writes down what it decided about a model.
//
// 🔴 Every field is OPTIONAL and a zero means "not declared", never "zero steps". The provider
// falls back to its family's own recipe field by field, so a row that declares only `steps`
// keeps the template's sampler rather than losing it to an empty string.
//
// What a human put here is the authority. The ingest form can fill these in from an author's
// prose, which is a heuristic over unstructured text — so it fills the FORM, in front of the
// person who presses the button, and never the row behind their back.
type EngineParams struct {
	Steps int `json:"steps,omitempty"`
	// CFG is the guidance scale. Fractional on purpose: 4.5 and 3.5 are ordinary values and an
	// int field would silently floor them.
	CFG float64 `json:"cfg,omitempty"`
	// Sampler and Scheduler are ComfyUI's OWN spellings (`dpmpp_2m`, `karras`), not the display
	// names a model card uses ("DPM++ 2M Karras"). 🔴 The node input is an enumeration and
	// ComfyUI answers `Value not in list` for anything else — the same refusal SD3.5's missing
	// clip_g file produced (ADR 0072 P2 残作業 5) — so the provider validates these against its
	// own list and ignores what it does not recognise rather than sending a graph that cannot run.
	Sampler   string `json:"sampler,omitempty"`
	Scheduler string `json:"scheduler,omitempty"`
	// ClipSkip is the WebUI's "Clip skip", applied by the sd15 and sdxl templates as
	// CLIPSetLastLayer(-n). ComfyUI's SDXL encoders already stop at 2, so on sdxl only 3 and up
	// change the picture.
	ClipSkip int `json:"clip_skip,omitempty"`
	// Guidance is FLUX.1's distilled guidance (FluxGuidance), which a FLUX.1 model card prints as
	// its "CFG" or "Distilled CFG" — flux1 has no sampler cfg, so it cannot live in CFG above.
	Guidance float64 `json:"guidance,omitempty"`
	// Shift is anima's sampling shift (ModelSamplingAuraFlow); undeclared keeps the model's 3.
	Shift float64 `json:"shift,omitempty"`
	// Weight is a LoRA row's recommended strength, and the one field here that is about an
	// adapter rather than a checkpoint. The provider already defaults an unstated weight to 1
	// (comfyResolveLoras) — this is what the catalogue puts between those two.
	Weight float64 `json:"weight,omitempty"`
}

// EngineModelFile is one S3 object a model is made of.
//
// Flag is empty for a single-file model — the engine's own `-m` takes it — and holds the
// LITERAL flag for each part of a split one (`--vae`, `--clip_l`, `--t5xxl`,
// `--diffusion-model`). Storing the flag rather than a role name keeps the engine's spelling
// out of the sidecar: sd.cpp mixes `--clip_l` with `--diffusion-model`, and a mapping table
// would be a second place to keep that in step.
//
// There is no local path. The box mirrors the bucket — `image/checkpoints/x.safetensors`
// becomes `/models/image/checkpoints/x.safetensors` — which is both what makes the active set
// small enough for SSM's 4,096 characters and what makes the tree ComfyUI can read unchanged
// (ADR 0071 decision 6).
type EngineModelFile struct {
	Flag  string `json:"flag,omitempty"`
	S3Key string `json:"s3Key"`
	// Bytes is the object's size, as whoever staged it declared it. It buys one thing: the panel can say what
	// enabling this model adds to the next cold start, which for the llm role is real money
	// (S3 to EBS ran at 104-147 MB/s, so 18.5 GB is three minutes of a $1.26/hour box).
	// The storage endpoint may report S3's current ContentLength independently; zero here still
	// means undeclared, and undeclared prints nothing rather than "+0 s".
	Bytes int64 `json:"bytes,omitempty"`
	// Source is where THIS file came from, in the same vocabulary as EngineModel.Source
	// (`hf:<repo>/<file>`, `civitai:<id>`, a URL). The row carries one too, and it answers a
	// different question — two of them, neither of which the row's own can answer:
	//
	//   - 🔴 the row's Source describes the file that CREATED the row and nothing else. An attach
	//     writes only the file (AppendEngineModelFile), so a four-file FLUX.1 row carries one
	//     `hf:black-forest-labs/FLUX.1-dev/...` line that a reader takes for the provenance of
	//     all four. The parts are routinely from different repositories — the t5xxl in ADR 0072's
	//     table is not published by the people who published the diffusion model — and once the
	//     ingest job ages out of engine_ingest_jobs nothing else remembers;
	//   - and a row is only ever as true as its last whole-row write. Now that one file can be
	//     REPLACED under a row that keeps everything else, the row's own source names a file that
	//     is no longer there. Per file is the only place the answer stays correct.
	//
	// No migration: `files` is a JSON column (migration 0057), so an old row simply has no
	// per-file source. Absent for a seeded row, for one registered by hand, and for everything
	// taken in before this existed — which is why nothing reads it as "unknown vendor".
	Source string `json:"source,omitempty"`
	// ArtifactIdentity pins Source to the exact immutable object selected upstream: HF includes
	// repository, file, resolved revision and sha256; Civitai includes version, file and sha256;
	// a URL includes the URL and sha256. Reuse is allowed only when this exact value matches the
	// newly resolved source. Source alone is intentionally insufficient because its legacy HF
	// spelling has no revision and its Civitai spelling has no selected filename.
	//
	// This rides inside the existing files JSON column, so old rows deserialize with it absent.
	// Absence must never be reconstructed from Source: an ambiguous legacy identity is unknown,
	// not permission to reuse whatever bytes happen to occupy the key now.
	ArtifactIdentity string `json:"artifactIdentity,omitempty"`
	// VaeBundled is whether THIS file carries the VAE tensors its family decodes with — "yes",
	// "no", or absent for "nobody read the header" (engine_safetensors.go). It decides the one
	// fault that otherwise costs a checkpoint switch to discover: an SDXL checkpoint published
	// without a VAE validates, loads, and then fails every request inside ComfyUI.
	//
	// Per FILE and not per row, for the same reason Source above is: a row's checkpoint can be
	// replaced under everything else it holds, and the fact belongs to the bytes that were read.
	// Absent is load-bearing — a row is only ever marked broken on an explicit "no".
	VaeBundled string `json:"vaeBundled,omitempty"`
}

// EngineModelKV is the attention geometry a row's VRAM estimate is computed from, as the one
// thing a file replacement may have to rewrite.
//
// It exists as a type rather than four arguments because the reason to pass it is "this file's
// header was read again"; passing nil says the opposite — leave what the row has alone — and a
// replacement of a text encoder has no opinion about the checkpoint's attention heads.
type EngineModelKV struct {
	Layers, HeadsKV, KeyLen, ValueLen int
	// The hybrid modifiers, travelling with the four they correct — see EngineModel's
	// KVNextN/KVFullAttnInterval. Left out here, a re-read header would keep the row's stale
	// divisor and go back to over-estimating by four.
	NextN, FullAttnInterval int
	// The per-layer widths and the sliding window — see EngineModel's KVFullWidth. Left out
	// here, a re-read header would keep describing the previous file's layers.
	FullWidth, SWAWidth, SlidingWindow int
	// And the model's own ceiling, for the same reason: a file swapped for another quantisation
	// of a DIFFERENT model publishes a different maximum, and a row left with the previous one
	// is re-fitted against a limit its weights never had.
	Ceiling int
}

// EngineModelStore is the catalogue. Two writers reach it — an administrator's toggle and the
// ingest job's state transitions — which is why it is a table and not one JSON value in
// SettingsStore (ADR 0072 open question 5).
type EngineModelStore interface {
	// ListEngineModels returns one role's catalogue, ordered by id. role=="" spans every role.
	ListEngineModels(ctx context.Context, role string) ([]EngineModel, error)
	// PutEngineModel inserts or replaces one row wholesale.
	PutEngineModel(ctx context.Context, m EngineModel) error
	// CreateEngineModel inserts one row only when the role/id is still free. Reports false on
	// conflict, so a validated ingest cannot overwrite a row created while it was in flight.
	CreateEngineModel(ctx context.Context, m EngineModel) (bool, error)
	// AppendEngineModelFile adds one file to an existing row, so a split model can be built up
	// part by part by the ingest instead of being re-typed through the register route. Reports
	// false when there is no such row.
	AppendEngineModelFile(ctx context.Context, role, id string, f EngineModelFile) (bool, error)
	// ReplaceEngineModelFile puts one file in the place of the one the row holds under the same
	// flag, and changes nothing else about the row. It is what makes a model's file correctable
	// at all: appending refuses a flag that is taken, and the UNLABELLED slot — the checkpoint
	// itself — can never be appended to, so before this the only way to swap a quantisation was
	// to forget the row and build it again, losing the licence acceptance, the params, the
	// enabled state and the provenance with it.
	//
	// `kv` rewrites the attention geometry when the replacement's own header was read; nil
	// leaves it, because a row whose text encoder changed has no new answer about its
	// checkpoint's attention heads. 🔴 Passing the geometry matters: the row carries ONE set
	// and only the create path ever wrote it, so a gguf swapped for another quantisation would
	// otherwise keep the old file's geometry and go on estimating VRAM off a file that is gone.
	//
	// Reports false when there is no such row or the row no longer declares that slot.
	ReplaceEngineModelFile(ctx context.Context, role, id string, f EngineModelFile, kv *EngineModelKV) (bool, error)
	// MoveEngineModelFile rewrites the file a row holds at `fromKey` into `f` — a new role and a
	// new key for the same bytes — and changes nothing else. It is what repairs a file staged in
	// a directory its loader does not enumerate: the bytes are relocated inside the bucket by the
	// ingest task and the declaration has to follow them, which neither appending (the old file
	// would stay) nor replacing (it finds its slot BY the flag that is changing) can express.
	//
	// Reports false when there is no such row or nothing at `fromKey`, true when the row already
	// holds `f` — a job reconciler may see the same finished task twice.
	MoveEngineModelFile(ctx context.Context, role, id, fromKey string, f EngineModelFile) (bool, error)
	// SetEngineModelEnabled toggles one row. Reports false when there is no such row, so a
	// caller can answer 404 rather than 200 for a model that does not exist.
	SetEngineModelEnabled(ctx context.Context, role, id string, enabled bool) (bool, error)
	// SetEngineModelSelected makes one row THE selected checkpoint of its role, clearing the
	// others in the same statement — an image role with two selected rows has no answer to
	// "what will the engine start with", and the exclusivity has to hold across two writers.
	SetEngineModelSelected(ctx context.Context, role, id string) (bool, error)
	// SetEngineModelDefault is the same exclusivity for the llm role's default model.
	SetEngineModelDefault(ctx context.Context, role, id string) (bool, error)
	// SetEngineModelBaseModel corrects the declared checkpoint family of one row, which is the
	// one field a row can be missing while looking complete (ADR 0072 decision 2).
	SetEngineModelBaseModel(ctx context.Context, role, id, baseModel string) (bool, error)
	// SetEngineModelParams replaces one row's generation defaults. A nil p clears them, which
	// is the only way back to "use the family's own recipe" once something has been declared.
	SetEngineModelParams(ctx context.Context, role, id string, p *EngineParams) (bool, error)
	// SetEngineModelNegativePrompt corrects the row's own negative prompt — what this checkpoint
	// should never be asked to draw (ADR 0072 follow-up, negative prompts).
	SetEngineModelNegativePrompt(ctx context.Context, role, id, negative string) (bool, error)
	// SetEngineModelTrainedWords replaces the words an adapter answers to. An empty list is a
	// real value — "this row has no trigger" — so the caller passing nil clears the column
	// rather than leaving the previous words in place (ADR 0081 decision 5).
	SetEngineModelTrainedWords(ctx context.Context, role, id string, words []string) (bool, error)
	// SetEngineModelDisplay writes what the publisher calls this model and where its example
	// image is (ADR 0088). All four together, empty included: they are one snapshot of one
	// upstream document, and "the page no longer publishes a picture" is an answer.
	SetEngineModelDisplay(ctx context.Context, role, id string, d EngineModelDisplay) (bool, error)
	// SetEngineModelWindow corrects the declared window. BOTH columns, because the catalogue
	// carries max_output_tokens only when context_tokens is above zero: a row that moved one of
	// them alone is one the panel cannot explain.
	SetEngineModelWindow(ctx context.Context, role, id string, contextTokens, maxOutputTokens int) (bool, error)
	// SetEngineModelGeometry writes only the columns a header read produced. Targeted because
	// the read is a network round trip and a whole-row write of the pre-read snapshot would
	// revert anything that changed while it was in flight.
	// `files` is the declaration the caller read the geometry's file out of, compared on write: a
	// replacement that landed in between must not have the previous file's shape stapled to it.
	SetEngineModelGeometry(ctx context.Context, role, id string, files []EngineModelFile, kv EngineModelKV) (bool, error)
	// SetEngineModelVram writes the operator's own VRAM measurement; 0 withdraws it and puts the
	// row back on the floor its files imply.
	SetEngineModelVram(ctx context.Context, role, id string, vramMiB int) (bool, error)
	DeleteEngineModel(ctx context.Context, role, id string) (bool, error)
}

const engineModelCols = `role, id, kind, files, enabled, selected, is_default, args,
	context_tokens, max_output_tokens, sizes, description, vram_mib,
	license, license_name, license_url, model_precision, base_model,
	license_accepted_by, license_accepted_at, license_accepted_tenant, license_accepted_license,
	commercial_use, source, kv_layers, kv_heads_kv, kv_key_len, kv_value_len,
	kv_nextn, kv_full_attn_interval, kv_full_width, kv_swa_width, kv_sliding_window,
	context_ceiling,
	negative_prompt, trained_words, params,
	display_name, version_name, preview_url, thumb_url, created_at, updated_at`

func (s *SQL) ListEngineModels(ctx context.Context, role string) ([]EngineModel, error) {
	q := `SELECT ` + engineModelCols + ` FROM engine_models`
	var args []any
	if role != "" {
		q += ` WHERE role=?`
		args = append(args, role)
	}
	q += ` ORDER BY role, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineModel
	for rows.Next() {
		var (
			m                        EngineModel
			files, argsJSON, sizes   string
			params, trained          string
			enabled, selected, isDef int
		)
		if err := rows.Scan(&m.Role, &m.ID, &m.Kind, &files, &enabled, &selected, &isDef, &argsJSON,
			&m.ContextTokens, &m.MaxOutputTokens, &sizes, &m.Description, &m.VramMiB,
			&m.License, &m.LicenseName, &m.LicenseURL, &m.Precision, &m.BaseModel,
			&m.LicenseAcceptedBy, &m.LicenseAcceptedAt,
			&m.LicenseAcceptedTenant, &m.LicenseAcceptedLicense,
			&m.CommercialUse, &m.Source,
			&m.KVLayers, &m.KVHeadsKV, &m.KVKeyLen, &m.KVValueLen,
			&m.KVNextN, &m.KVFullAttnInterval, &m.KVFullWidth, &m.KVSWAWidth, &m.KVSlidingWindow,
			&m.ContextCeiling,
			&m.NegativePrompt, &trained, &params,
			&m.DisplayName, &m.VersionName, &m.PreviewURL, &m.ThumbURL,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled, m.Selected, m.Default = enabled != 0, selected != 0, isDef != 0
		// A row whose JSON does not parse is returned with that list empty rather than
		// failing the whole listing: one unreadable catalogue entry must not take the admin
		// panel — the only place it can be deleted from — down with it.
		_ = json.Unmarshal([]byte(files), &m.Files)
		_ = json.Unmarshal([]byte(argsJSON), &m.Args)
		_ = json.Unmarshal([]byte(sizes), &m.Sizes)
		_ = json.Unmarshal([]byte(trained), &m.TrainedWords)
		// Absent rather than zeroed when the column is empty or unreadable: a row that declares
		// no parameters must reach the provider as "use the family's recipe", and an
		// EngineParams full of zeros says the same thing only as long as nobody adds a field
		// whose zero value means something.
		if strings.TrimSpace(params) != "" {
			var p EngineParams
			if json.Unmarshal([]byte(params), &p) == nil && p != (EngineParams{}) {
				m.Params = &p
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQL) PutEngineModel(ctx context.Context, m EngineModel) error {
	now := NowTS()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	files := jsonList(m.Files)
	args := jsonList(m.Args)
	sizes := jsonList(m.Sizes)
	trained := jsonList(m.TrainedWords)
	params := ""
	if m.Params != nil && *m.Params != (EngineParams{}) {
		if b, err := json.Marshal(m.Params); err == nil {
			params = string(b)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_models(`+engineModelCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(role, id) DO UPDATE SET
		   kind=excluded.kind, files=excluded.files, enabled=excluded.enabled,
		   selected=excluded.selected, is_default=excluded.is_default, args=excluded.args,
		   context_tokens=excluded.context_tokens, max_output_tokens=excluded.max_output_tokens,
		   sizes=excluded.sizes, description=excluded.description, vram_mib=excluded.vram_mib,
		   license=excluded.license, license_name=excluded.license_name,
		   license_url=excluded.license_url, model_precision=excluded.model_precision,
		   base_model=excluded.base_model, license_accepted_by=excluded.license_accepted_by,
		   license_accepted_at=excluded.license_accepted_at,
		   license_accepted_tenant=excluded.license_accepted_tenant,
		   license_accepted_license=excluded.license_accepted_license,
		   commercial_use=excluded.commercial_use, source=excluded.source,
		   kv_layers=excluded.kv_layers, kv_heads_kv=excluded.kv_heads_kv,
		   kv_key_len=excluded.kv_key_len, kv_value_len=excluded.kv_value_len,
		   kv_nextn=excluded.kv_nextn, kv_full_attn_interval=excluded.kv_full_attn_interval,
		   kv_full_width=excluded.kv_full_width, kv_swa_width=excluded.kv_swa_width,
		   kv_sliding_window=excluded.kv_sliding_window,
		   context_ceiling=excluded.context_ceiling,
		   negative_prompt=excluded.negative_prompt, trained_words=excluded.trained_words,
		   params=excluded.params, display_name=excluded.display_name,
		   version_name=excluded.version_name, preview_url=excluded.preview_url,
		   thumb_url=excluded.thumb_url, updated_at=excluded.updated_at`,
		m.Role, m.ID, m.Kind, files, boolInt(m.Enabled), boolInt(m.Selected), boolInt(m.Default), args,
		m.ContextTokens, m.MaxOutputTokens, sizes, m.Description, m.VramMiB,
		m.License, m.LicenseName, m.LicenseURL, m.Precision, m.BaseModel,
		m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.LicenseAcceptedTenant, m.LicenseAcceptedLicense,
		m.CommercialUse, m.Source,
		m.KVLayers, m.KVHeadsKV, m.KVKeyLen, m.KVValueLen,
		m.KVNextN, m.KVFullAttnInterval, m.KVFullWidth, m.KVSWAWidth, m.KVSlidingWindow,
		m.ContextCeiling,
		m.NegativePrompt, trained, params,
		m.DisplayName, m.VersionName, m.PreviewURL, m.ThumbURL, m.CreatedAt, now)
	return err
}

// CreateEngineModel is the ingest writer. Unlike the administrative Put above, a collision is
// not an edit: the new bytes and their licence were chosen for a different row, so the caller
// must keep the row that won the race and report the conflict.
func (s *SQL) CreateEngineModel(ctx context.Context, m EngineModel) (bool, error) {
	now := NowTS()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	params := ""
	if m.Params != nil && *m.Params != (EngineParams{}) {
		if b, err := json.Marshal(m.Params); err == nil {
			params = string(b)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_models(`+engineModelCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(role, id) DO NOTHING`,
		m.Role, m.ID, m.Kind, jsonList(m.Files), boolInt(m.Enabled), boolInt(m.Selected), boolInt(m.Default), jsonList(m.Args),
		m.ContextTokens, m.MaxOutputTokens, jsonList(m.Sizes), m.Description, m.VramMiB,
		m.License, m.LicenseName, m.LicenseURL, m.Precision, m.BaseModel,
		m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.LicenseAcceptedTenant, m.LicenseAcceptedLicense,
		m.CommercialUse, m.Source,
		m.KVLayers, m.KVHeadsKV, m.KVKeyLen, m.KVValueLen,
		m.KVNextN, m.KVFullAttnInterval, m.KVFullWidth, m.KVSWAWidth, m.KVSlidingWindow,
		m.ContextCeiling,
		m.NegativePrompt, jsonList(m.TrainedWords), params,
		m.DisplayName, m.VersionName, m.PreviewURL, m.ThumbURL, m.CreatedAt, now)
	return affected(res, err)
}

// AppendEngineModelFile adds one file to a row that already exists, which is what lets a SPLIT
// model be assembled by taking its parts in one at a time (ADR 0072 decision 2's
// `text_encoders/`). Without it an ingest could only ever create a row of one file, and a
// four-file FLUX.1 had to be staged as three throwaway rows and then re-typed through
// `POST /models`.
//
// An optimistic compare-and-swap rather than PutEngineModel: the row carries a licence
// acceptance, a source and an enabled flag that the ingest that is appending knows nothing
// about. Comparing the JSON read also keeps two concurrent parts from overwriting each other.
//
// A key already listed is a no-op reporting success: the caller is a job reconciler that may
// see the same finished task twice, and a second append would put the same file in the active
// set twice.
var ErrEngineModelFileSlotTaken = errors.New("engine model file slot is already taken")

func (s *SQL) AppendEngineModelFile(ctx context.Context, role, id string, f EngineModelFile) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		for _, e := range files {
			if e.S3Key == f.S3Key {
				return true, nil
			}
			if strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				return false, ErrEngineModelFileSlotTaken
			}
		}
		files = append(files, f)
		updated, err := affected(s.db.ExecContext(ctx,
			`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
			jsonList(files), NowTS(), role, id, raw))
		if err != nil || updated {
			return updated, err
		}
	}
}

// ReplaceEngineModelFile swaps the file a row holds under one flag, leaving the rest of the row
// exactly as it is — which is the whole point of it existing.
//
// 🔴 The gap it closes: AppendEngineModelFile refuses a flag the row already declares, and the
// unlabelled slot is THE checkpoint, which cannot be appended to at all. So until this, changing
// which file a model reads meant forgetting the row and taking it in again — and the row is
// where the licence acceptance (a record of a human act), the family, the params, the enabled
// flag and the provenance live. Swapping a t5xxl for another quantisation threw all of them away.
//
// The same optimistic compare-and-swap as the append above, and for the same reason: an upsert
// through PutEngineModel would have to carry every other column back out and in again, while an
// unchecked read-modify-write could discard a part attached at the same time.
//
// A flag the row does not declare is NOT created here. "Replace what is there" and "add a part"
// are different acts with different refusals, and silently turning one into the other is how a
// typo in a flag becomes a row with two checkpoints.
func (s *SQL) ReplaceEngineModelFile(ctx context.Context, role, id string, f EngineModelFile, kv *EngineModelKV) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		at := -1
		for i, e := range files {
			if strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				at = i
				break
			}
		}
		if at < 0 {
			return false, nil
		}
		files[at] = f
		var updated bool
		var err error
		if kv == nil {
			updated, err = affected(s.db.ExecContext(ctx,
				`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
				jsonList(files), NowTS(), role, id, raw))
		} else {
			// Written even when it is all zeros: an unreadable header means the row's geometry is
			// now UNKNOWN. Keeping the previous file's numbers would describe bytes no longer used.
			updated, err = affected(s.db.ExecContext(ctx,
				`UPDATE engine_models SET files=?, kv_layers=?, kv_heads_kv=?, kv_key_len=?, kv_value_len=?,
				   kv_nextn=?, kv_full_attn_interval=?, kv_full_width=?, kv_swa_width=?, kv_sliding_window=?,
				   context_ceiling=?,
				   updated_at=? WHERE role=? AND id=? AND files=?`,
				jsonList(files), kv.Layers, kv.HeadsKV, kv.KeyLen, kv.ValueLen,
				kv.NextN, kv.FullAttnInterval, kv.FullWidth, kv.SWAWidth, kv.SlidingWindow, kv.Ceiling,
				NowTS(), role, id, raw))
		}
		if err != nil || updated {
			return updated, err
		}
	}
}

// MoveEngineModelFile rewrites the one file a row holds at `fromKey` — its role AND its key —
// leaving every other file and every other column alone. It is the catalogue half of relocating
// bytes inside the bucket, and it exists because neither of the two writers above can express it:
// appending would leave the old declaration behind (two files, one of them pointing at a key the
// move emptied) and replacing is keyed by the flag, which is the very thing that changes.
//
// The same optimistic compare-and-swap as its neighbours, and idempotent in the one way that
// matters: a reconciler that sees the same finished task twice finds the row already moved —
// `fromKey` gone, `f` present — and reports success rather than failing a job that did its work.
func (s *SQL) MoveEngineModelFile(ctx context.Context, role, id, fromKey string, f EngineModelFile) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		at, done := -1, false
		for i, e := range files {
			switch {
			case strings.TrimSpace(e.S3Key) == strings.TrimSpace(fromKey):
				at = i
			case strings.TrimSpace(e.S3Key) == strings.TrimSpace(f.S3Key) &&
				strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag):
				done = true
			}
		}
		if at < 0 {
			return done, nil
		}
		// 🔴 The destination role must be free, or the move would silently give the row two files
		// under one flag — the state AppendEngineModelFile refuses for the same reason.
		for i, e := range files {
			if i != at && strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				return false, ErrEngineModelFileSlotTaken
			}
		}
		files[at] = f
		updated, err := affected(s.db.ExecContext(ctx,
			`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
			jsonList(files), NowTS(), role, id, raw))
		if err != nil || updated {
			return updated, err
		}
	}
}

func (s *SQL) SetEngineModelEnabled(ctx context.Context, role, id string, enabled bool) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET enabled=?, updated_at=? WHERE role=? AND id=?`,
		boolInt(enabled), NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelBaseModel corrects ONE column, and exists because a row can be complete in
// every other way and still unusable: ComfyUI picks its workflow graph from the family and
// refuses to guess one, so a seeded row (the seed cannot know a family) or one written before
// the family was validated has to be fixable without being re-typed. A targeted UPDATE rather
// than a read-modify-write through PutEngineModel: the row carries a licence acceptance, a
// source and a sha256 that nothing else in this request knows, and a round trip would have to
// carry them back out and in again to change one word.
// SetEngineModelParams writes the same one column, and is targeted for the same reason as the
// base model next door: the row carries a licence acceptance and a sha256 that a request
// changing two numbers knows nothing about.
func (s *SQL) SetEngineModelParams(ctx context.Context, role, id string, p *EngineParams) (bool, error) {
	raw := ""
	if p != nil && *p != (EngineParams{}) {
		b, err := json.Marshal(p)
		if err != nil {
			return false, err
		}
		raw = string(b)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET params=?, updated_at=? WHERE role=? AND id=?`,
		raw, NowTS(), role, id)
	return affected(res, err)
}

func (s *SQL) SetEngineModelBaseModel(ctx context.Context, role, id, baseModel string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET base_model=?, updated_at=? WHERE role=? AND id=?`,
		baseModel, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelWindow corrects the declared window, and writes BOTH columns because they are
// one declaration: the catalogue only carries max_output_tokens when context_tokens is above
// zero, so a row that moved one of them alone is a row the panel cannot explain.
//
// It exists because the window is what a wrong row costs money to discover: measured on a
// borrowed llm engine, a 17 GB model whose context_tokens still said 262144 allocated a 16 GiB
// KV cache and died with `cudaMalloc failed: out of memory` — four minutes after a GPU box had
// been bought. Re-registering the row to change one number would carry its licence acceptance,
// its source and its sha256 back out and in again.
func (s *SQL) SetEngineModelWindow(ctx context.Context, role, id string, contextTokens, maxOutputTokens int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET context_tokens=?, max_output_tokens=?, updated_at=? WHERE role=? AND id=?`,
		contextTokens, maxOutputTokens, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelGeometry writes ONLY the attention geometry a header read produced.
//
// 🔴 A targeted UPDATE and not a whole-row Put, and that is the entire point of it existing.
// The read it follows is a network round trip to object storage, so the row the caller holds is
// already seconds old by the time there is anything to write — and a full upsert of that stale
// snapshot silently reverts whatever anybody else changed in between. This is the same
// read-modify-write hazard a plain file rewrite has, and the same answer: write the columns you
// learned about and leave every other one to whoever owns it.
// 🔴 `files` is a compare-and-swap, not a filter. The geometry describes ONE file's header, and
// between the read that produced it and this write another session can Replace the main GGUF —
// or delete the row and register the same id over different bytes. An unconditional UPDATE then
// staples the old file's attention shape onto the new file's row, which is a wrong VRAM answer
// that nothing afterwards contradicts. The same optimistic swap ReplaceEngineModelFile makes,
// for the same reason: `false` means somebody moved first and the caller simply does not write.
func (s *SQL) SetEngineModelGeometry(ctx context.Context, role, id string, files []EngineModelFile, kv EngineModelKV) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET kv_layers=?, kv_heads_kv=?, kv_key_len=?, kv_value_len=?,
		   kv_nextn=?, kv_full_attn_interval=?, kv_full_width=?, kv_swa_width=?, kv_sliding_window=?,
				   context_ceiling=?, updated_at=?
		 WHERE role=? AND id=? AND files=?`,
		kv.Layers, kv.HeadsKV, kv.KeyLen, kv.ValueLen,
		kv.NextN, kv.FullAttnInterval, kv.FullWidth, kv.SWAWidth, kv.SlidingWindow, kv.Ceiling, NowTS(), role, id, jsonList(files))
	return affected(res, err)
}

// SetEngineModelVram writes the operator's own VRAM measurement, and 0 withdraws it — which
// puts the row back on the floor derived from its files rather than on a number nobody stands
// behind any more.
func (s *SQL) SetEngineModelVram(ctx context.Context, role, id string, vramMiB int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET vram_mib=?, updated_at=? WHERE role=? AND id=?`,
		vramMiB, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelNegativePrompt corrects ONE column, for the same reason SetEngineModelBaseModel
// does: it is a field an administrator tunes after watching what the checkpoint actually draws,
// and a read-modify-write through PutEngineModel would carry a licence acceptance, a source and
// a sha256 back out and in again to change one sentence.
func (s *SQL) SetEngineModelNegativePrompt(ctx context.Context, role, id, negative string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET negative_prompt=?, updated_at=? WHERE role=? AND id=?`,
		negative, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelTrainedWords replaces the words an adapter answers to, one targeted column like
// the negative prompt above and for the same reason: the row carries a licence acceptance and a
// sha256 that a request correcting a trigger word knows nothing about.
//
// An empty list is written as `[]` rather than refused — "this adapter has no trigger" is a real
// answer, and it is the only way back from a word Civitai published that turns out to be wrong.
func (s *SQL) SetEngineModelTrainedWords(ctx context.Context, role, id string, words []string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET trained_words=?, updated_at=? WHERE role=? AND id=?`,
		jsonList(words), NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelDisplay writes what the publisher calls this model and where its example image
// is (ADR 0088), one targeted column group like the two above and for the same reason: the row
// carries a licence acceptance and a sha256 that a re-read of a model page knows nothing about.
//
// All four are written together, empty included. They are one snapshot of one upstream document
// — keeping an old picture beside a new name would show a model that is half of each — and
// "the page no longer publishes an image" is an answer this has to be able to record.
func (s *SQL) SetEngineModelDisplay(ctx context.Context, role, id string, d EngineModelDisplay) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET display_name=?, version_name=?, preview_url=?, thumb_url=?,
		 updated_at=? WHERE role=? AND id=?`,
		d.DisplayName, d.VersionName, d.PreviewURL, d.ThumbURL, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelSelected and SetEngineModelDefault clear the role's other rows in the SAME
// transaction as the one they set. Doing it in two calls leaves a window in which the image
// role has two selected checkpoints, and the sidecar reading the active set in that window
// builds a command line for whichever it saw first — a failure that reproduces once.
func (s *SQL) SetEngineModelSelected(ctx context.Context, role, id string) (bool, error) {
	return s.setEngineModelExclusive(ctx, "selected", role, id)
}

func (s *SQL) SetEngineModelDefault(ctx context.Context, role, id string) (bool, error) {
	return s.setEngineModelExclusive(ctx, "is_default", role, id)
}

// setEngineModelExclusive is shared by the two above. `col` is never caller-supplied — the two
// exported wrappers are the only callers and both pass a literal.
func (s *SQL) setEngineModelExclusive(ctx context.Context, col, role, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := NowTS()
	if _, err := tx.ExecContext(ctx,
		`UPDATE engine_models SET `+col+`=0, updated_at=? WHERE role=? AND `+col+`<>0`, now, role); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE engine_models SET `+col+`=1, enabled=1, updated_at=? WHERE role=? AND id=?`, now, role, id)
	ok, err := affected(res, err)
	if err != nil || !ok {
		return false, err
	}
	return true, tx.Commit()
}

func (s *SQL) DeleteEngineModel(ctx context.Context, role, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM engine_models WHERE role=? AND id=?`, role, id)
	return affected(res, err)
}

// jsonList marshals a slice to the text the column holds. A nil slice becomes `[]` rather than
// `null`, so a reader never has to tell the two apart.
func jsonList(v any) string {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// affected turns "did that statement touch a row" into the bool the callers answer 404 from.
// RowsAffected is not supported by every driver; an error from it is reported as "yes", because
// the statement itself succeeded and claiming the row was absent would be the wrong lie.
func affected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, aerr := res.RowsAffected()
	if aerr != nil {
		return true, nil
	}
	return n > 0, nil
}
