// Package imagegen turns a prompt into image files inside the Workspace (ADR 0069).
//
// The shape is the one ADR 0013 established for TTS: an interface plus a chooser, with
// everything that is not "make pixels" kept out of the providers. A provider receives a
// Request and returns bytes; storage, naming, retention, warnings, usage recording and the
// MCP surface are this package's core (ADR 0069 decision 2). A provider that writes files
// where it likes, or invents its own path convention, is a provider that cannot be swapped.
//
// It lives in the Agent rather than the Control Plane because the P0 route is the Codex CLI
// and the user's ChatGPT login, both of which exist only inside the container, and because
// the artifact has to land on a disk the Console's file API can read.
package imagegen

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Op is the operation a request asks for. The whole vocabulary exists from day one even
// though the Codex route only implements generate: the Bedrock and Stability catalogues are
// mostly editing operations, and introducing the axis later means every provider has already
// grown its own private parameters and the shared vocabulary dies (ADR 0069 decision 6).
type Op string

const (
	OpGenerate         Op = "generate"
	OpEdit             Op = "edit"
	OpInpaint          Op = "inpaint"
	OpOutpaint         Op = "outpaint"
	OpRemoveBackground Op = "remove-background"
	OpUpscale          Op = "upscale"
)

// AllOps is the vocabulary in the order the MCP schema advertises it.
var AllOps = []Op{OpGenerate, OpEdit, OpInpaint, OpOutpaint, OpRemoveBackground, OpUpscale}

func ValidOp(op Op) bool {
	for _, o := range AllOps {
		if o == op {
			return true
		}
	}
	return false
}

// Request is what the caller asks for — not what it will necessarily get. size / background /
// count stay in the vocabulary even while only the Codex route exists, and an unmet request
// comes back in Result.Warnings saying what actually happened (ADR 0069 decision 7).
type Request struct {
	Op     Op
	Prompt string
	// Size is "<w>x<h>" or "auto". Provider-checked, never enforced here: the core does not
	// silently resample to hit an exact size, because that would trade a real dependency for
	// a promise the provider never made.
	Size string
	// AspectRatio is "<w>:<h>" or "auto" — a SEPARATE axis from Size, not a spelling of it.
	// The agy route takes a ratio and has no size parameter at all, while the Codex route takes
	// neither; folding one into the other would make a provider that honours the ratio look
	// like one that ignores the size (ADR 0069 decision 5, per (provider, model) capability).
	AspectRatio string
	// Background is "auto" | "opaque" | "transparent". gpt-image-2 has no transparency at all.
	Background string
	Count      int
	// Inputs are absolute paths of reference images, Mask an absolute path of a mask image.
	// Both are provider input only; neither is written to.
	Inputs []string
	Mask   string
	// Model is provider-specific. Empty means the provider's own default — no model id is
	// hard-coded into a caller, because ids move (ADR 0069 Context: "treat the vendor figures
	// as dated").
	Model string
	// Seed pins the sampler's starting noise, so that two requests differing in ONE thing can be
	// compared (ADR 0072 phase P3's completion definition needs exactly that: the same prompt and
	// the same seed, with the LoRA and without).
	//
	// A POINTER rather than "0 means unset", because 0 is a perfectly good seed and a caller who
	// pins it deserves to get it rather than a random one. nil is the default and keeps the old
	// behaviour: every route that has a seed at all picks a fresh one per request.
	Seed *int64
	// Loras are the fine-tunes to apply on top of Model, in the order given (ADR 0072 decision
	// 5, phase P3). Only the fleet's own engines have any; a route with none reports the request
	// back as a warning rather than dropping it silently.
	Loras []LoraRef
}

// LoraRef is one LoRA a request asks for: a name out of Caps.Loras, and how strongly to apply
// it. A LoRA whose base model does not match the chosen checkpoint is refused by the PROVIDER
// while it assembles the request (ADR 0072 decision 5, レビュー決定 5) — the Control Plane never
// sees it, because the pairing lives inside a workflow graph the gateway must not read.
type LoraRef struct {
	Name string
	// Weight is 0-2, and 0 means "not stated": a LoRA asked for at strength zero is a LoRA that
	// does nothing, which nobody means, so the provider reads it as decision 5's default of 1.
	Weight float64
}

// Image is one produced picture, in memory. A provider hands these back and never decides
// where they live.
type Image struct {
	Bytes  []byte
	MIME   string
	Width  int
	Height int
}

// Usage is the driver-model consumption a route incurred producing the images. It is the
// tokens only: the plan quota the image itself consumes is NOT expressible in tokens on the
// ChatGPT-login route, and is deliberately left out rather than zero-filled (ADR 0069
// decision 9).
type Usage struct {
	In          int
	Out         int
	CacheRead   int
	CacheCreate int
	// Measured is false when the route reports no tokens at all, so that "zero" and "not
	// measured" can never be confused in the ledger.
	Measured bool
}

// Result is what a provider returns. Provenance is part of it (ADR 0069 decision 11): a
// generated image means the prompt left the container to a named service, and the answer to
// "which one, on what model, in what region" has to be auditable.
type Result struct {
	Images   []Image
	Provider string
	Model    string
	Region   string
	// Destination says, in words a person reads, where the prompt actually went. A provider
	// id answers that only for someone who knows the vocabulary, and the difference that
	// matters here — a vendor's API against a box this deployment runs itself — is exactly
	// the one `sdcpp` does not spell out. Empty when the provider id already says it.
	Destination string
	// Warnings say what the provider could not honour, in the caller's language-neutral terms
	// ("size=1024x1024 requested, 1254x1254 produced"). Never a silent downgrade.
	Warnings []string
	// CostUSD is an estimate and 0 means "not estimable on this route" — it is never a claim
	// that the call was free.
	CostUSD float64
	Usage   Usage
}

// Caps is what a provider can do for ONE (provider, model) pair — never per provider.
// gpt-image-2 has no transparent background while gpt-image-1.5 does, transparency is
// preview-only on generate and absent on edit, and Bedrock's catalogue differs by region. A
// provider that reports one fixed capability set will lie the first time a model or region is
// switched (ADR 0069 decision 5).
type Caps struct {
	Ops []Op
	// Sizes is the exact sizes the caller may pick. EMPTY MEANS THE CALLER CANNOT PICK — on
	// the Codex route the size is decided by the model and measured to ignore what was asked
	// for, so an empty list is the honest answer, not a missing one.
	Sizes []string
	// AspectRatios is the exact ratios the caller may pick ("16:9"). Empty means the caller
	// cannot pick, exactly as with Sizes — and the two are independent: the agy route has
	// ratios and no sizes, the Codex route has neither. A ratio list stuffed into Sizes would
	// advertise "16:9" as a dimension and be wrong in both directions.
	AspectRatios []string
	Backgrounds  []string
	MaxCount     int
	MaxInputs    int
	// Seed is whether Request.Seed reaches this route at all. FALSE is the common answer and it
	// is not a gap: the vendor routes take no seed, and sd-server's OpenAI-compatible endpoint
	// documents only prompt/n/size (checked against its own source — see the ADR 0069 follow-up
	// for why the one undocumented channel that would carry it is deliberately not used).
	Seed bool
	// Loras is every fine-tune this provider will accept in Request.Loras (ADR 0072 decision 5,
	// phase P3). Empty means the caller cannot pick, exactly as with Sizes.
	//
	// It is EVERY enabled LoRA, not the ones that fit `model`, even though Caps is per (provider,
	// model) and could narrow it. The tool schema this feeds is built once per tools/list, before
	// any model is chosen, and decision 5's revision says so outright: an enum cannot depend on
	// another argument. So each entry carries its own BaseModel for the caller to read, and the
	// provider refuses a pairing that does not match while it assembles the request.
	Loras []LoraInfo
}

// LoraInfo is one LoRA as the tool surface needs to see it: the name to send back, the line an
// agent reads when choosing, and the checkpoint family it may be combined with.
type LoraInfo struct {
	Name        string
	Description string
	// BaseModel is decision 2's family label ("sdxl", "flux1", …) — the same vocabulary a
	// checkpoint declares, which is what makes the two comparable at all. Empty for a catalogue
	// row that declares none, which the provider refuses rather than guessing at: an SD1.5 LoRA
	// on an SDXL checkpoint produces a quietly wrong picture, never an error.
	BaseModel string
}

func (c Caps) Supports(op Op) bool {
	for _, o := range c.Ops {
		if o == op {
			return true
		}
	}
	return false
}

// Provider is one image service. Generate takes a context from the start so that the
// submit-then-poll providers (Replicate, FLUX) can arrive later as a second tool without
// reshaping this interface (ADR 0069, open question 1).
type Provider interface {
	ID() string
	Caps(model string) Caps
	Ready(ctx context.Context) bool
	Generate(ctx context.Context, req Request) (Result, error)
}

// ModelInfo is one checkpoint a caller may name in Request.Model, as the MCP surface needs to
// see it: an id to send back, a line an agent reads when choosing, and whether the engine
// happens to have it loaded right now.
type ModelInfo struct {
	ID          string
	Description string
	// Warm is true for at most one model per provider — the one a request naming none would
	// get (ADR 0072 decision 7). Advertised so an agent can say "the warm one is fine" instead
	// of naming a cold checkpoint and paying a switch it did not need to ask for.
	Warm bool
}

// ModelLister is an OPTIONAL capability a Provider may implement: "here is more than one
// checkpoint you may ask for by name". Kept off the core Provider interface because every
// existing provider (codex, agy, sdcpp) has exactly one answer for any model argument — codex
// and agy do not expose a choice at all, and sdcpp holds one checkpoint chosen at start — so
// forcing them to implement a list-of-one would be a required method with no real information
// in it. comfy (ADR 0072 P2) is the first provider for which this is ever more than one entry.
type ModelLister interface {
	Models(ctx context.Context) []ModelInfo
}

// Provider ids. The id is the wire value the MCP surface and the ledger both carry.
const (
	ProviderCodex = "codex"
	ProviderAgy   = "agy"
	// ProviderSdcpp is the fleet's OWN engine (ADR 0071): stable-diffusion.cpp on a GPU this
	// deployment pays for, reached through the Control Plane's engine gateway.
	ProviderSdcpp = "sdcpp"
	// ProviderComfy is the fleet's own engine, the ComfyUI alternative (ADR 0072 decision 4,
	// phase P2). Same transport as sdcpp — the Control Plane's engine gateway — but it holds
	// several checkpoints at once and switches per REQUEST, which is what makes `model` a real
	// choice instead of a fixed fact about the deployment. A deployment runs the `image` role
	// as sdcpp OR comfy, never both (60-engines.yaml's `ImageEngine`), so exactly one of the
	// two ever answers Ready().
	ProviderComfy = "comfy"
)

// providerOrder is the BUILT-IN order "auto" walks. The first two entries are Tier-1 (ADR 0069
// decision 3): each runs on a login the container already holds, and neither costs a new secret
// or an egress allowlist entry. The third is the fleet's own hardware (ADR 0071), present only
// in a deployment that stood an image engine up.
//
// sdcpp is first where it exists, and the reason is whose account pays: the other two spend a
// MEMBER's plan quota — invisibly, three to five times faster than a text turn, which is why
// the whole feature is off by default (decision 8) — while a deployment that stood up the image
// engine has already decided to pay for that hardware itself. It also honours more of the
// request than either: exact sizes (measured), plus edit and inpaint, which neither of the
// others can do at all. It is simply absent from `Ready` where no engine is deployed, which is
// most deployments, so this does not change what anyone gets today.
//
// agy before codex because it HONOURS MORE OF THE REQUEST: its aspect ratio reaches the tool
// (measured), while the Codex route lets the caller choose no dimension at all. The first
// version of this list put codex first on the grounds that a reordered default would move an
// existing user's generation onto a different plan's quota — a real objection, and one that
// only applies once there are such users. There are none yet (this has not shipped), so the
// default is chosen on the merits instead, while that is still free. A stored
// `imageProviderOrder` outranks this list, so anyone who does have a preference keeps it.
var providerOrder = providerIDsOf(providerRanks)

// providerRank is one provider's place in the built-in order plus the ONE fact that decides
// where an unranked provider is inserted: whose hardware or plan pays for the picture.
//
// It is declared here, once, rather than tested for with an id comparison wherever it matters.
// The distinction is not cosmetic — it decides whether an unattended call spends money this
// deployment already committed to, or a MEMBER's personal plan quota — and an `id == "comfy"`
// written into a condition is exactly how the next provider gets forgotten.
//
// It hangs off the provider rather than off Caps because Caps is per (provider, model): whose
// wallet pays does not change with the checkpoint, and putting it there would invite a model
// that answers differently from its own provider.
type providerRank struct {
	ID string
	// Fleet is "this deployment's own hardware serves it" — the engines behind ADR 0071 and
	// ADR 0072. False means an external service on somebody's personal plan.
	Fleet bool
}

// providerRanks is the single declaration of the built-in order AND of which providers the fleet
// serves itself. Adding a provider means adding one line here; nothing else reads the ids.
var providerRanks = []providerRank{
	{ID: ProviderSdcpp, Fleet: true},
	{ID: ProviderComfy, Fleet: true},
	{ID: ProviderAgy},
	{ID: ProviderCodex},
}

func providerIDsOf(ranks []providerRank) []string {
	out := make([]string, 0, len(ranks))
	for _, r := range ranks {
		out = append(out, r.ID)
	}
	return out
}

// providerIsFleet answers whether the fleet's own hardware serves this provider. Unknown ids are
// NOT fleet: an id nobody declared is not something this deployment can be said to pay for.
func providerIsFleet(id string) bool {
	for _, r := range providerRanks {
		if r.ID == id {
			return r.Fleet
		}
	}
	return false
}

// ProviderOrderPref is the user's own preference order, installed by the ui-prefs layer (the
// same hook shape as Enabled). nil, or a list that names nothing known, simply means the
// built-in order.
var ProviderOrderPref func() []string

// effectiveOrder normalizes the stored preference into a TOTAL order: unknown ids and duplicates
// are dropped, and every provider the preference does not mention is inserted around it. The same
// shape as main's agentOrderPref, and for the same reason — a partial or stale list (written
// before a provider existed) must still rank every provider, or adding one would make it
// unreachable until the user re-saved their settings.
//
// 🔴 A provider the preference does not name goes to the FRONT when the fleet serves it and to
// the back when it does not, instead of all of them going to the back.
//
// That rule exists because appending everything was measured doing real harm (ADR 0072's
// 2026-09-11 hardware follow-up). The dev deployment's stored value was
// `["sdcpp","agy","codex"]`, written before `comfy` existed. comfy was therefore appended LAST,
// so `auto` walked sdcpp (absent on that deployment) → agy → codex → comfy: a call that named no
// provider spent a member's personal plan before it ever reached the GPU the deployment is
// already paying for. Nobody had edited a setting; the stored list changed meaning on the day a
// provider was added, which is the one thing a normalization step is there to prevent.
//
// What the preference DOES say is untouched: ids it names keep their relative order, including a
// fleet provider the user deliberately ranked last. This only decides where the ones it never
// mentioned land.
func effectiveOrder() []string {
	seen := map[string]bool{}
	known := map[string]bool{}
	for _, id := range providerOrder {
		known[id] = true
	}
	var chosen []string
	if ProviderOrderPref != nil {
		for _, id := range ProviderOrderPref() {
			if known[id] && !seen[id] {
				seen[id] = true
				chosen = append(chosen, id)
			}
		}
	}
	// The unmentioned ones, split by who pays and each half kept in the built-in order.
	var fleet, external []string
	for _, id := range providerOrder {
		if seen[id] {
			continue
		}
		seen[id] = true
		if providerIsFleet(id) {
			fleet = append(fleet, id)
			continue
		}
		external = append(external, id)
	}
	out := make([]string, 0, len(providerOrder))
	out = append(out, fleet...)
	out = append(out, chosen...)
	return append(out, external...)
}

// Providers returns the registered providers, in providerOrder. Built fresh on each call so a
// changed environment (a Codex login that arrived after boot, an engine stack deployed since)
// is picked up, and a var so a test can drive Run without a Codex CLI on PATH.
var Providers = func() []Provider {
	return []Provider{newSdcppProvider(), newComfyProvider(), newCodexProvider(), newAgyProvider()}
}

// chooseImageProviders decides what "auto" (the default) routes to, in order — the same shape
// as chooseTTSProvider in control-plane/tts.go, widened to a LIST because readiness is checked
// before the call while exhaustion only shows up during it. A provider that says it is ready
// and then fails is exactly the case a single choice cannot survive.
//
// An explicit choice is honoured as-is EVEN WHEN IT IS NOT READY, and it never falls through
// to another: the caller named a provider, and that provider's own error ("codex is not logged
// in") is a better answer than silently producing an image on a different service, billed to a
// different account. Only auto walks the list. Empty means nothing can serve the request.
func chooseImageProviders(pref string, req Request, order []string, ready map[string]bool, caps func(id string) Caps) []string {
	if pref != "" && pref != "auto" {
		return []string{pref}
	}
	var out []string
	for _, id := range order {
		if ready[id] && caps(id).Supports(req.Op) {
			out = append(out, id)
		}
	}
	return out
}

// Job is one core-side generation: which session asked, what it asked for, and which provider
// it wants. Session and SID are separate on purpose — the ledger's ref is the session NAME,
// while the directory is keyed by the session UUID, the same split session_paste.go makes.
type Job struct {
	Session string
	SID     string
	Pref    string
	Request Request
}

// StoredFile is one image after the core has put it where it belongs.
type StoredFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Bytes  int64  `json:"bytes"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Stored is the core's answer: files on disk plus the provenance and the warnings.
type Stored struct {
	Files       []StoredFile `json:"files"`
	Provider    string       `json:"provider"`
	Model       string       `json:"model,omitempty"`
	Region      string       `json:"region,omitempty"`
	Destination string       `json:"destination,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
	CostUSD     float64      `json:"cost_usd,omitempty"`
}

// ErrNoProvider is the refusal when nothing can serve the request — no provider is ready, or
// none of them does the requested operation. Typed so the HTTP layer can answer 503 rather
// than reporting a configuration gap as an internal failure.
var ErrNoProvider = errors.New("no image provider can serve this request")

// ErrUnknownProvider is an explicit pref naming something that does not exist.
var ErrUnknownProvider = errors.New("unknown image provider")

// Run is the core entry point: choose a provider, run it, store the images, record the usage.
// Everything a provider must not decide happens here.
func Run(ctx context.Context, job Job) (Stored, error) {
	req := normalizeRequest(job.Request)
	provs := map[string]Provider{}
	ready := map[string]bool{}
	for _, p := range Providers() {
		provs[p.ID()] = p
		ready[p.ID()] = p.Ready(ctx)
	}
	capsOf := func(id string) Caps {
		p, ok := provs[id]
		if !ok {
			return Caps{}
		}
		return p.Caps(req.Model)
	}
	candidates := chooseImageProviders(job.Pref, req, effectiveOrder(), ready, capsOf)
	if len(candidates) == 0 {
		return Stored{}, ErrNoProvider
	}

	var attempts []error
	for _, name := range candidates {
		p, ok := provs[name]
		if !ok {
			return Stored{}, fmt.Errorf("%w: %s", ErrUnknownProvider, name)
		}
		// An explicit pref reaches here even for an op it cannot do, so that the refusal comes
		// from the named provider rather than from a chooser the caller cannot see.
		if !p.Caps(req.Model).Supports(req.Op) {
			return Stored{}, fmt.Errorf("%w: %s cannot do %s", ErrNoProvider, name, req.Op)
		}
		if err := ctx.Err(); err != nil {
			return Stored{}, err
		}

		started := time.Now()
		res, err := p.Generate(ctx, req)
		// Record on every path, including the failed one: an attempt that burned driver tokens
		// and produced nothing still consumed the user's plan, and a row with ok:false is what
		// keeps that visible (ADR 0029 §3). Recording INSIDE the loop is what makes a
		// fall-through cost two honest rows rather than one that hides the wasted attempt.
		recordUsage(ctx, job, name, res, err == nil && len(res.Images) > 0, started)
		if err == nil && len(res.Images) == 0 {
			err = errors.New("the provider returned no image")
		}
		if err != nil {
			attempts = append(attempts, fmt.Errorf("%s: %w", name, err))
			continue // the next provider in the order, if the caller left the choice to us
		}

		files, err := storeImages(job.SID, res.Images)
		if err != nil {
			// A storage failure is OURS, not the provider's: the picture exists and trying a
			// second provider would spend more quota to hit the same broken disk.
			return Stored{}, err
		}
		warnings := append(fallbackWarnings(name, attempts), res.Warnings...)
		return Stored{
			Files:       files,
			Provider:    res.Provider,
			Model:       res.Model,
			Region:      res.Region,
			Destination: res.Destination,
			Warnings:    append(warnings, requestWarnings(req, res, p.Caps(req.Model))...),
			CostUSD:     res.CostUSD,
		}, nil
	}
	// Every candidate failed. Report them all: "codex is out of quota, and the local engine is
	// not running" is actionable in a way that either half alone is not.
	return Stored{}, errors.Join(attempts...)
}

// fallbackWarnings says out loud that this picture was NOT made by the provider the order
// picked, and names what went wrong with the one(s) ahead of it.
//
// It exists because of a measured accident (ADR 0071 P1, 2026-09-07): the fleet's own engine
// failed on a cold start and `auto` fell through to agy, which produced the image against a
// MEMBER's Antigravity plan. Everything worked as designed and the result said
// `provider: agy` — but nobody was told that a plan had been spent because something failed,
// as opposed to because that was the plan. Whose wallet paid is exactly what the built-in
// order is there to decide, so silently inverting it is the thing that must not be silent.
//
// A warning rather than a refusal: on a deployment whose engine really is down, falling
// through is the RIGHT answer and refusing would just mean no picture.
func fallbackWarnings(used string, attempts []error) []string {
	if len(attempts) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(attempts))
	for _, err := range attempts {
		reasons = append(reasons, err.Error())
	}
	return []string{fmt.Sprintf(
		"fell back to %s because the provider(s) ahead of it failed: %s — this ran on a different account's plan than the preferred route",
		used, strings.Join(reasons, "; "))}
}

// normalizeRequest fills in the defaults the wire may omit. It never narrows what was asked
// for — an impossible size stays in the request so the provider can report it back as a
// warning rather than have it quietly rewritten here.
func normalizeRequest(r Request) Request {
	if r.Op == "" {
		r.Op = OpGenerate
	}
	if r.Count <= 0 {
		r.Count = 1
	}
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.Size = strings.TrimSpace(r.Size)
	r.AspectRatio = strings.TrimSpace(r.AspectRatio)
	r.Background = strings.TrimSpace(r.Background)
	// Whitespace only: an unknown name stays in the request so the provider refuses it BY NAME
	// rather than having it quietly disappear here — the same rule the size follows.
	if len(r.Loras) > 0 {
		loras := make([]LoraRef, 0, len(r.Loras))
		for _, l := range r.Loras {
			if l.Name = strings.TrimSpace(l.Name); l.Name != "" {
				loras = append(loras, l)
			}
		}
		r.Loras = loras
	}
	return r
}

// requestWarnings is the core's own comparison of what was asked for against what arrived.
// The provider reports what IT knows it could not honour; this catches the rest — most
// importantly a count that came back short, which no provider can see as a failure because
// each image it did produce is fine.
func requestWarnings(req Request, res Result, caps Caps) []string {
	var out []string
	// A route with no aspect-ratio list cannot have honoured one, and unlike the size there is
	// no produced dimension to catch it after the fact — a 16:9 request answered with a square
	// picture is only visibly wrong to someone who knows what they asked for.
	if r := req.AspectRatio; r != "" && r != "auto" && len(caps.AspectRatios) == 0 {
		out = append(out, fmt.Sprintf("aspect_ratio=%s requested, but this route cannot choose an aspect ratio", r))
	}
	// Same shape, and for the same reason a ratio needs one: a picture generated without the
	// LoRA that was asked for looks fine, so nothing else would ever say it was dropped. A route
	// that CAN apply them refuses an unusable pairing instead (ADR 0072 decision 5).
	if len(req.Loras) > 0 && len(caps.Loras) == 0 {
		names := make([]string, 0, len(req.Loras))
		for _, l := range req.Loras {
			names = append(names, l.Name)
		}
		out = append(out, fmt.Sprintf("loras=%s requested, but this route cannot apply a LoRA", strings.Join(names, ", ")))
	}
	// A dropped seed is the most invisible of the three: the picture is fine, and the caller only
	// finds out when the SECOND request — the whole point of pinning one — comes back different.
	if req.Seed != nil && !caps.Seed {
		out = append(out, fmt.Sprintf(
			"seed=%d requested, but this route cannot pin a seed — two calls with the same seed will not match", *req.Seed))
	}
	if n := len(res.Images); req.Count > 0 && n != req.Count {
		out = append(out, fmt.Sprintf("count=%d requested, %d produced", req.Count, n))
	}
	if w, h, ok := parseSize(req.Size); ok {
		for _, img := range res.Images {
			if img.Width > 0 && img.Height > 0 && (img.Width != w || img.Height != h) {
				out = append(out, fmt.Sprintf("size=%s requested, %dx%d produced", req.Size, img.Width, img.Height))
				break
			}
		}
	}
	return out
}

// parseSize reads "<w>x<h>". "auto", "" and anything unparseable report ok=false — they are
// not a size to compare against, not an error.
func parseSize(s string) (w, h int, ok bool) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(s)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// recordUsage writes the ledger row for one generation (ADR 0069 decision 9 / ADR 0029 §3).
//
// The driver turn's tokens are exact when the route reports them, but the plan quota the
// IMAGE consumes is not expressible in tokens and is not recorded — hence measured=partial
// even on a fully successful run. Reading such a row as the whole consumption would understate
// it, and zero-filling the missing part would be worse: it would claim the image was free.
//
// chosen is the provider Run picked. A provider only stamps its own id on a Result it actually
// produced, so a request refused before any work started (an unsupported op, a mask on a route
// with no mask input) would otherwise leave the row's kind column empty.
func recordUsage(ctx context.Context, job Job, chosen string, res Result, ok bool, started time.Time) {
	tag := usagex.Tag{Feature: usagex.FeatureToolImagegen, Trigger: usagex.TriggerUser, Ref: job.Session}
	call := usagex.Call{
		Kind:     usageKindOf(res.Provider, chosen),
		ModelReq: res.Model,
		OK:       ok,
		CostUSD:  res.CostUSD,
		Measured: usagex.MeasuredPartial,
	}
	call.SetTotals(res.Usage.In, res.Usage.Out, res.Usage.CacheRead, res.Usage.CacheCreate)
	if !res.Usage.Measured {
		// Nothing at all came back from the route: say none rather than partial, so a
		// provider that reports no tokens is not mistaken for one that reports some.
		call.Measured = usagex.MeasuredNone
	}
	for _, img := range res.Images {
		call.Images++
		call.Pixels += img.Width * img.Height
	}
	usagex.RecordCall(usagex.WithTag(ctx, tag), &call, started)
}

// usageKindOf is the ledger's kind column: what actually ran, not what was requested. The
// Codex route really is a codex process, so it is attributed to codex; a provider that is a
// plain HTTP call to a vendor is not an agent kind at all and carries its own id.
//
// It is deliberately NOT the calling session's kind. A claude session that generates an image
// spends the ChatGPT plan, not Claude's, and filing the row under claude would put that
// consumption on the wrong plan's line. The calling session is on the row as `ref`.
func usageKindOf(provider, chosen string) string {
	if provider == "" {
		provider = chosen
	}
	switch provider {
	case ProviderCodex:
		return session.KindCodex
	case ProviderAgy:
		// The two spellings are identical today, which is exactly why the mapping is written
		// down: the provider id is a wire value of this package and the kind is the fleet's
		// agent-kind enum, and a rename of either must not silently file the row elsewhere.
		return agyUsageKind
	}
	return provider
}
