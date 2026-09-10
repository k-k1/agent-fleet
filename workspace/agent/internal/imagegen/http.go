package imagegen

// The Agent's REST face for image generation (ADR 0069 decision 8). Its only caller is the
// session-side `af` MCP server, which reaches it over agentBaseURL() the way every other
// session tool does — mcpx cannot import main, and the provider work has to happen in the
// Agent, where the container's credentials and disk are.
//
// Neither route is proxied by the Control Plane: nothing in the Console calls them, so they
// stay off the CP's agent-proxy allowlist deliberately rather than by omission.

import (
	"errors"
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
}

// modelStatus is one entry of providerStatus.Models — see imagegen.ModelInfo, which this rides
// unchanged from.
type modelStatus struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Warm        bool   `json:"warm,omitempty"`
}

// loraStatus is one entry of providerStatus.Loras — see imagegen.LoraInfo. baseModel rides along
// because the tool schema cannot narrow the enum per chosen checkpoint (Caps.Loras), so the
// caller is the one that has to read which family each belongs to.
type loraStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	BaseModel   string `json:"baseModel,omitempty"`
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
		// Only when there is a REAL choice (ADR 0072 decision 5's own rule for `model`, the
		// same one `provider` already follows) — a list of zero or one is not something a
		// caller can meaningfully pick between, and advertising it anyway would put an enum in
		// the tool schema that never has more than its own default in it.
		if ml, ok := p.(ModelLister); ok {
			if models := ml.Models(r.Context()); len(models) > 1 {
				st.Models = make([]modelStatus, 0, len(models))
				for _, m := range models {
					st.Models = append(st.Models, modelStatus{ID: m.ID, Description: m.Description, Warm: m.Warm})
				}
			}
		}
		out.Providers = append(out.Providers, st)
		if !out.Ready {
			out.Provider, out.Ready = st.ID, true
			out.Service = st.Service
			out.Model, out.Ops, out.AspectRatios = st.Model, st.Ops, st.AspectRatios
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
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
	case ProviderSdcpp:
		return "Stable Diffusion（このフリート自身の GPU。外部サービスではない）"
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
	case ProviderSdcpp:
		// Not a driver model but the CHECKPOINT the engine was started with — the only model
		// this route has, and the one its Caps are keyed to. Answered from the stack's
		// declaration, so asking costs nothing and does not wake the box.
		return sdcppDriverModel()
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
	// Loras are the fine-tunes to apply (ADR 0072 decision 5, phase P3). Whether they fit the
	// chosen checkpoint is the PROVIDER's call, not this layer's — see comfyResolveLoras.
	Loras []loraRequest `json:"loras"`
	// Seed is a POINTER on the wire too: `"seed": 0` and an absent key are different requests,
	// and collapsing them here would make seed 0 unpinnable.
	Seed *int64 `json:"seed"`
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
			Op: op, Prompt: body.Prompt, Size: body.Size, AspectRatio: body.AspectRatio,
			Background: body.Background, Count: body.Count, Inputs: body.Inputs,
			Mask: body.Mask, Model: body.Model, Loras: loras, Seed: body.Seed,
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
