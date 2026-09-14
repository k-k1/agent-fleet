package imagegen

// The Agent's REST face for image generation (ADR 0069 decision 8). Its only caller is the
// session-side `af` MCP server, which reaches it over agentBaseURL() the way every other
// session tool does — mcpx cannot import main, and the provider work has to happen in the
// Agent, where the container's credentials and disk are.
//
// Neither route is proxied by the Control Plane: nothing in the Console calls them, so they
// stay off the CP's agent-proxy allowlist deliberately rather than by omission.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Enabled is the opt-in gate, installed by the ui-prefs layer at startup (the same hook shape
// as mcpreg.PeerMessagingEnabled). nil means OFF, which is also the product default: this
// spends the user's ChatGPT plan quota from sessions that are not Codex sessions, invisibly.
//
// The gate is applied HERE as well as in the MCP server's advertised set. The MCP surface
// alone would leave the REST route open to anything holding AGENT_TOKEN — which every
// session's own MCP server does — so the preference would be one curl away from bypass.
var Enabled func() bool

func enabled() bool { return Enabled != nil && Enabled() }

// statusResponse answers "should this session be offered the tool, and by what route". The
// RULE that turns it into a yes or no lives in the MCP server (mcpx), where the tool list is
// built; this endpoint reports facts.
type statusResponse struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"` // the effective provider, "" when none is ready
	Ready    bool   `json:"ready"`
	Kind     string `json:"kind,omitempty"` // the asking session's agent kind, "" when unknown
	Model    string `json:"model,omitempty"`
	// Service is the effective provider's image SERVICE, repeated from Providers for the same
	// reason the other flat fields are.
	Service string   `json:"service,omitempty"`
	Ops     []string `json:"ops,omitempty"`
	// AspectRatios is the effective provider's own list, so the MCP schema can offer the
	// parameter only where it actually reaches the tool. Empty means the tool must not
	// advertise it at all rather than accept it and drop it.
	AspectRatios []string `json:"aspectRatios,omitempty"`
	// Order is the effective provider order, so the answer to "why did it route there" is
	// readable without guessing at a preference file, and a settings UI has something to
	// render when there is more than one provider to rank.
	Order []string `json:"order,omitempty"`
	// Providers is EVERY ready provider, in the effective order, each with its own capability
	// list. The flat fields above describe only the first one — which was enough while the
	// caller could not choose, and stopped being enough the moment `generate_image` grew a
	// `provider` argument: a tool that offers a choice has to advertise what each choice can
	// do, or the enum is a guess.
	Providers []providerStatus `json:"providers,omitempty"`
}

// providerStatus is one ready provider as the tool surface needs to see it.
type providerStatus struct {
	ID string `json:"id"`
	// Service is the image service this route reaches, in the words a person asks for it by.
	// The id alone is a CLI name, and nothing downstream can decode it: a codex session whose
	// only route is `agy` was measured answering that "the Gemini route is not available in
	// this session" while holding exactly that route (2026-09-08).
	Service      string   `json:"service,omitempty"`
	Model        string   `json:"model,omitempty"`
	Ops          []string `json:"ops,omitempty"`
	AspectRatios []string `json:"aspectRatios,omitempty"`
	// Models is every checkpoint this provider may be asked for BY NAME (ADR 0072 decision 5,
	// phase P2) — empty for a provider that does not implement ModelLister at all (codex, agy)
	// or that currently has zero or one (nothing to choose between). comfy is the first
	// provider for which this is ever more than one entry.
	Models []modelStatus `json:"models,omitempty"`
	// Loras is every fine-tune this provider will accept (ADR 0072 decision 5, phase P3). Unlike
	// Models a single entry is still a real choice — with it or without it are two different
	// pictures — so there is no "more than one" rule here.
	Loras []loraStatus `json:"loras,omitempty"`
	// Seed is whether this route lets the caller pin the sampler's seed. Only the fleet's own
	// ComfyUI route does, so the tool offers the argument only where it reaches something.
	Seed bool `json:"seed,omitempty"`
	// Negative is whether ANY model on this route samples with a negative branch — a union, not
	// the default model's answer, because the argument is offered per route while the capability
	// is per model (see HandleStatus).
	Negative bool `json:"negative,omitempty"`
	// Strength is whether this route lets the caller say how much of the input picture an edit
	// changes. No union is needed: it is per provider, not per model.
	Strength bool `json:"strength,omitempty"`
	// The rest is the MEMBER-facing catalogue (ADR 0081 decision 5), present only for a provider
	// that builds the graph — the vendor routes have no sampler list and no per-model recipe, so
	// their entries keep the short shape they always had.
	//
	// It rides on this route rather than on a new, browser-authenticated one on the Control
	// Plane. A second projection of the same rows is a second thing to keep in step, and the
	// sessionWire lesson is that a field missing from a relay vanishes with nobody noticing.
	Samplers   []string `json:"samplers,omitempty"`
	Schedulers []string `json:"schedulers,omitempty"`
	// NegativeAlways is what this deployment's administrator excludes from every picture. Shown
	// as a fixed chip the member cannot remove, with its origin, rather than merged invisibly.
	NegativeAlways string `json:"negative_always,omitempty"`
	// LoraWeightMax is the ceiling one adapter may be asked for at. There is no default weight to
	// report: no column holds one, and the Agent uses 1 when nobody states one.
	LoraWeightMax float64 `json:"lora_weight_max,omitempty"`
	// TypicalMS is how long a picture usually takes on this route, as a moving average of what
	// has actually finished. 0 means nothing has been measured yet, which is a different
	// statement from "instant".
	TypicalMS int64 `json:"typical_ms,omitempty"`
	// WakeMS is the last observed cold start, so the header can say "the engine starts on the
	// first job; usually N minutes" from a measurement rather than a guess.
	WakeMS int64 `json:"wake_ms,omitempty"`
}

// modelStatus is one entry of providerStatus.Models — see imagegen.ModelInfo, which this rides
// unchanged from.
type modelStatus struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Warm        bool   `json:"warm,omitempty"`
	// The rest is ADR 0081 decision 5's widening, and is filled in only for a provider that
	// implements StudioLister. An agent reading this route for the MCP tool sees the same three
	// fields it always did; a form reading it sees what it needs to render placeholders and to
	// grey out what the family does not read.
	Family string   `json:"family,omitempty"`
	Sizes  []string `json:"sizes,omitempty"`
	// Params are the EFFECTIVE defaults (family recipe ← catalogue row), so a placeholder is
	// what will run rather than a number from some other family's recipe.
	Params *EngineParams `json:"params,omitempty"`
	// Negative is the row's own recommended negative prompt, shown as the administrator's, not
	// merged into the member's text.
	Negative string `json:"negative,omitempty"`
	// Knobs is the subset of `steps cfg sampler scheduler negative` this family reads. The form
	// disables the rest on THIS word; a second table in the Console could disagree with the
	// graphs, and the two must not be able to.
	Knobs       []string `json:"knobs,omitempty"`
	LicenseName string   `json:"license_name,omitempty"`
	LicenseURL  string   `json:"license_url,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	// TypicalMS is how long a picture on THIS checkpoint usually takes, measured.
	TypicalMS int64 `json:"typical_ms,omitempty"`
}

// loraStatus is one entry of providerStatus.Loras — see imagegen.LoraInfo. baseModel rides along
// because the tool schema cannot narrow the enum per chosen checkpoint (Caps.Loras), so the
// caller is the one that has to read which family each belongs to.
type loraStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	BaseModel   string `json:"baseModel,omitempty"`
	// TrainedWords are the trigger words the adapter's author published (ADR 0081 decision 5).
	// The most common "the LoRA does nothing" is a missing trigger, and nothing else in this
	// answer can fix it.
	//
	// The family stays on the existing `baseModel` key rather than gaining ADR 0081's
	// `base_model` spelling: it is the same fact, already on this route, and a second key for it
	// would be one more thing to keep in step for a consistency nothing reads.
	TrainedWords []string `json:"trained_words,omitempty"`
	// Weight is the strength the catalogue row declares, 0 for "not declared" — in which case the
	// Agent applies 1. There is no default-weight column, so 0 must not be shown as a number.
	Weight float64 `json:"weight,omitempty"`
}

// HandleStatus answers GET /imagegen/status?session=<name>.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	out := statusResponse{Enabled: enabled(), Order: effectiveOrder()}
	if name := r.URL.Query().Get("session"); session.ValidName(name) {
		if m, ok := session.ReadMeta(name); ok {
			out.Kind = m.Kind
		}
	}
	// Every ready provider, in the effective order. The FIRST is what auto would route to, and
	// is repeated in the flat fields; the rest are what an explicit `provider` can name.
	byID := map[string]Provider{}
	for _, p := range Providers() {
		byID[p.ID()] = p
	}
	for _, id := range out.Order {
		p, ok := byID[id]
		if !ok || !p.Ready(r.Context()) {
			continue
		}
		caps := p.Caps("")
		st := providerStatus{
			ID:           p.ID(),
			Service:      serviceLabelOf(p.ID()),
			Model:        driverModelOf(p.ID()),
			AspectRatios: caps.AspectRatios,
		}
		for _, op := range caps.Ops {
			st.Ops = append(st.Ops, string(op))
		}
		for _, l := range caps.Loras {
			st.Loras = append(st.Loras, loraStatus{Name: l.Name, Description: l.Description, BaseModel: l.BaseModel})
		}
		st.Seed = caps.Seed
		st.Negative = caps.Negative
		st.Strength = caps.Strength
		// Only when there is a REAL choice (ADR 0072 decision 5's own rule for `model`, the
		// same one `provider` already follows) — a list of zero or one is not something a
		// caller can meaningfully pick between, and advertising it anyway would put an enum in
		// the tool schema that never has more than its own default in it.
		if ml, ok := p.(ModelLister); ok {
			models := ml.Models(r.Context())
			if len(models) > 1 {
				st.Models = make([]modelStatus, 0, len(models))
				for _, m := range models {
					st.Models = append(st.Models, modelStatus{ID: m.ID, Description: m.Description, Warm: m.Warm})
				}
			}
			// A UNION over the models, unlike everything else here, because `negative_prompt` is
			// offered per ROUTE while Caps.Negative is per model: on comfy the warm default may
			// be a distilled family that cannot take one while an SDXL checkpoint next to it can.
			// Asking caps.Negative alone would take the argument away from a session that can use
			// it by naming the other model. A model that cannot still warns (requestWarnings).
			for _, m := range models {
				if p.Caps(m.ID).Negative {
					st.Negative = true
					break
				}
			}
		}
		// The member-facing widening (ADR 0081 decision 5). It REPLACES the model list above for
		// a provider that has one, because the studio's list is the one that withholds a model
		// the engine could not actually run — the catalogue already refuses those at generation
		// time, and offering a form that produces an error after a cold start is worse than not
		// offering it.
		applyStudio(r.Context(), p, &st)
		out.Providers = append(out.Providers, st)
		if !out.Ready {
			out.Provider, out.Ready = st.ID, true
			out.Service = st.Service
			out.Model, out.Ops, out.AspectRatios = st.Model, st.Ops, st.AspectRatios
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// applyStudio folds the member-facing catalogue into one provider's status entry, and does
// nothing at all for a provider that does not implement StudioLister — the vendor routes keep
// the short shape an MCP caller already reads.
//
// 🔴 The model list it writes is NOT the union with the one above: a model the studio withholds
// (no declared family, or a family whose files the catalogue never declared) is one the engine
// would refuse after paying for a cold start, and a form that offers it is a form with a button
// that cannot work.
func applyStudio(ctx context.Context, p Provider, st *providerStatus) {
	s, ok := studioOf(ctx, p)
	if !ok {
		return
	}
	st.Samplers, st.Schedulers = s.Samplers, s.Schedulers
	st.NegativeAlways, st.LoraWeightMax = s.NegativeAlways, s.LoraWeightMax
	st.TypicalMS, st.WakeMS = jobs.typicalFor(p.ID(), ""), jobs.observedWakeMS()
	st.Models = make([]modelStatus, 0, len(s.Models))
	for _, m := range s.Models {
		params := m.Params
		st.Models = append(st.Models, modelStatus{
			ID: m.ID, Description: m.Description, Warm: m.Warm,
			Family: m.Family, Sizes: m.Sizes, Params: &params, Negative: m.Negative,
			Knobs: m.Knobs, LicenseName: m.LicenseName, LicenseURL: m.LicenseURL,
			SourceURL: m.SourceURL, TypicalMS: jobs.typicalFor(p.ID(), m.ID),
		})
	}
	st.Loras = make([]loraStatus, 0, len(s.Loras))
	for _, l := range s.Loras {
		st.Loras = append(st.Loras, loraStatus{
			Name: l.Name, Description: l.Description, BaseModel: l.BaseModel,
			TrainedWords: l.TrainedWords, Weight: l.Weight,
		})
	}
}

// serviceLabelOf names the image SERVICE a provider reaches — not the CLI that drives it and
// not a model id. A caller asks for "GPT Image" or "Gemini", never for "codex" or "agy", and a
// model handed only the id has to guess the mapping; the one that guessed wrong concluded a
// service it could reach was unavailable. Brand names rather than model ids on purpose: ids
// move (ADR 0069 Context) and nothing here may depend on one staying valid.
//
// The plan each one spends is part of the label because it is the difference that decides
// between two routes when the caller does have a choice.
func serviceLabelOf(id string) string {
	switch id {
	case ProviderCodex:
		return "GPT Image（OpenAI。利用者の ChatGPT プランを消費）"
	case ProviderAgy:
		// The nickname is here because it is what a member says out loud, and matching the
		// request to a route is the whole job of this label. It stays a NICKNAME for the family
		// rather than a tier ("Nano Banana Pro" is the pro image model, this route is on a flash
		// one) — naming a tier would be a claim about a model id that moves.
		return "Gemini の画像生成（通称 Nano Banana。Google。利用者の Antigravity/Gemini プランを消費）"
	case ProviderOpenAICompat:
		// Unlike the other cases, this id names a PROTOCOL, not a service (ADR 0083 decision 2):
		// the engine table row behind it can be this fleet's own GPU, an operator's LAN box, or a
		// metered vendor endpoint paid by an API key, and the id alone cannot tell those apart.
		// Say what is true of the protocol and let Destination (Result.Destination) carry the
		// specific row's answer, rather than asserting "not external" for a row that may be.
		return "OpenAI 互換の画像サーバー（宛先はエンジン表の行次第。このフリート自身の GPU のこともあれば、鍵で払う外部サービスのこともある）"
	case ProviderComfy:
		// Deliberately not "this fleet's own GPU": since ADR 0076 the same route also reaches a
		// ComfyUI on the operator's LAN, and the part that decides between routes is that no
		// caller's plan is spent either way.
		return "ComfyUI（このフリートのエンジン。外部サービスではない）"
	}
	return ""
}

// driverModelOf reports the model a generation would run on, per provider. "" for a provider
// that is not driven by a model of ours to name.
func driverModelOf(id string) string {
	switch id {
	case ProviderCodex:
		return codexDriverModel()
	case ProviderAgy:
		return agyDriverModel()
	case ProviderOpenAICompat:
		// Not a driver model but the default checkpoint: the row's first declared model id, which
		// is also the only one on a single-checkpoint server. Answered from the row's own
		// declaration, so asking costs nothing and does not wake anything.
		return openaiCompatDriverModel()
	case ProviderComfy:
		// Not a driver model either, and unlike sdcpp not the only one this route has: it is the
		// checkpoint a request naming none would run on, with the rest carried in
		// providerStatus.Models. Answered from what the Control Plane already told us, so asking
		// costs nothing and does not wake the box.
		return comfyDriverModel()
	}
	return ""
}

type generateRequest struct {
	Session     string   `json:"session"`
	Provider    string   `json:"provider"`
	Op          string   `json:"op"`
	Prompt      string   `json:"prompt"`
	Size        string   `json:"size"`
	AspectRatio string   `json:"aspectRatio"`
	Background  string   `json:"background"`
	Count       int      `json:"count"`
	Inputs      []string `json:"inputs"`
	Mask        string   `json:"mask"`
	Model       string   `json:"model"`
	// NegativePrompt is what to keep OUT. It is added to the catalogue row's own negative and to
	// the engine's administrator list rather than replacing either (comfyNegativeFor).
	NegativePrompt string `json:"negativePrompt"`
	// Loras are the fine-tunes to apply (ADR 0072 decision 5, phase P3). Whether they fit the
	// chosen checkpoint is the PROVIDER's call, not this layer's — see comfyResolveLoras.
	Loras []loraRequest `json:"loras"`
	// Seed is a POINTER on the wire too: `"seed": 0` and an absent key are different requests,
	// and collapsing them here would make seed 0 unpinnable.
	Seed *int64 `json:"seed"`
	// Strength is how much of the input picture an edit changes, and a pointer for the same
	// reason as Seed — `"strength": 0` is a request, not an absence.
	Strength *float64 `json:"strength"`
}

type loraRequest struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// HandleGenerate answers POST /imagegen/generate. It blocks for the whole generation: P0 is
// synchronous by decision, because each poll of a job+poll shape would be a driver-model turn
// on the CALLER's plan, and no P0 provider is asynchronous by nature (ADR 0069, open
// question 1). The MCP server keeps its client's clock alive with notifications/progress
// while this runs.
func HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if !enabled() {
		httpx.WriteErr(w, http.StatusForbidden, "imagegen_disabled",
			"image generation is off (Settings > Agents)")
		return
	}
	var body generateRequest
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_prompt", "prompt is required")
		return
	}
	op := Op(strings.TrimSpace(body.Op))
	if op == "" {
		op = OpGenerate
	}
	if !ValidOp(op) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", "unknown op: "+string(op))
		return
	}
	if !session.ValidName(body.Session) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "session is required")
		return
	}
	// Refused by VALUE rather than clamped, which is the one place this layer narrows anything:
	// a strength outside the range is not a request a provider could report back honestly. 0 is
	// out too — ComfyUI's sampler returns the latent untouched at denoise 0, so it would spend a
	// GPU box on a VAE round-trip of a picture the caller already has.
	if s := body.Strength; s != nil && (*s <= 0 || *s > 1) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_strength",
			fmt.Sprintf("strength must be greater than 0 and at most 1 (got %g): 1 redraws the picture from the prompt alone, and small values keep more of the input", *s))
		return
	}
	meta, ok := session.ReadMeta(body.Session)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+body.Session)
		return
	}
	// A named provider may not be the caller's OWN CLI. The tool's enum already leaves it out,
	// but the advertised set is a scope boundary and a guessed name in tools/call must not cross
	// it: this session can make that picture with its own built-in tool, and going out through a
	// second process of the same CLI would spend the plan twice for it (ADR 0069 decision 8).
	if p := strings.TrimSpace(body.Provider); p != "" && p == meta.Kind {
		// The message names the SERVICE as well as the id, so the refusal cannot be read as
		// "that service is unreachable from here" — it is reachable, through this session's own
		// built-in tool, and that is the whole instruction.
		detail := ""
		if s := serviceLabelOf(p); s != "" {
			detail = " (" + s + ")"
		}
		httpx.WriteErr(w, http.StatusBadRequest, "imagegen_own_cli",
			"this session is a "+meta.Kind+" session: the "+p+" route"+detail+
				" is reachable through its OWN built-in image tool — use that rather than spending the same plan twice through the fleet one")
		return
	}

	loras := make([]LoraRef, 0, len(body.Loras))
	for _, l := range body.Loras {
		loras = append(loras, LoraRef{Name: l.Name, Weight: l.Weight})
	}
	job := Job{
		Session: body.Session,
		SID:     session.UUID(meta.Dir, body.Session),
		Pref:    body.Provider,
		Request: Request{
			Op: op, Prompt: body.Prompt, NegativePrompt: body.NegativePrompt,
			Size: body.Size, AspectRatio: body.AspectRatio,
			Background: body.Background, Count: body.Count, Inputs: body.Inputs,
			Mask: body.Mask, Model: body.Model, Loras: loras, Seed: body.Seed,
			Strength: body.Strength,
		},
	}
	out, err := Run(r.Context(), job)
	if err != nil {
		writeGenerateErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// writeGenerateErr keeps the reason on the wire. A generic 500 here would reach the model as
// "generation failed" with nothing to act on, and "you are not logged in to Codex" and "the
// prompt was refused" call for opposite responses.
func writeGenerateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoProvider):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "imagegen_no_provider", err.Error())
	case errors.Is(err, ErrUnknownProvider):
		httpx.WriteErr(w, http.StatusBadRequest, "imagegen_unknown_provider", err.Error())
	default:
		httpx.WriteErr(w, http.StatusBadGateway, "imagegen_failed", err.Error())
	}
}
