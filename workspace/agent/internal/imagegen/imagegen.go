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
	// NegativePrompt is what to keep OUT of the picture, and it is a SEPARATE axis from Prompt
	// rather than a phrasing of it: a diffusion sampler reaches it through the unconditional
	// branch of classifier-free guidance, which is not a thing a positive prompt can say
	// ("without text" in Prompt is conditioning ON text). Only a route that builds its own
	// sampler graph has that branch — the fleet's own ComfyUI — so everywhere else this comes
	// back as a warning rather than being folded into Prompt.
	NegativePrompt string
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
	// Strength is how much of the caller's own picture an edit changes: 0 keeps it, 1 ignores it
	// entirely. It is the ONE degree of freedom op=edit has, and it was a constant until the
	// 2026-09-13 follow-up — "correct this slightly" and "borrow the composition and draw the
	// rest again" were the same request, and no phrasing of Prompt, no seed and no negative
	// prompt could tell them apart.
	//
	// The direction is spelled out because upstream disagrees about it: diffusers' img2img
	// `strength` and ComfyUI's `denoise` run this way round, Stability's `image_strength` runs
	// the other.
	//
	// A POINTER, and unlike Seed not because 0 is usable — it is refused. It is so that 0 can be
	// refused BY VALUE: a caller who sends it means something ("change nothing"), and a plain
	// float64 would read that as "not given" and hand back a picture edited at the full default
	// amount, which is the opposite of what was asked for. nil is the only "not given" there is.
	//
	// Only op=edit reads it — see Caps.Strength for why inpaint must not.
	Strength *float64
	// Loras are the fine-tunes to apply on top of Model, in the order given (ADR 0072 decision
	// 5, phase P3). Only the fleet's own engines have any; a route with none reports the request
	// back as a warning rather than dropping it silently.
	Loras []LoraRef
	// Params is the caller's own sampler overlay — steps, cfg, sampler, scheduler (ADR 0081
	// decision 4). It shares the catalogue's shape because it is merged into the SAME place the
	// catalogue row is: family recipe ← catalogue row ← this, field by field.
	//
	// Both surfaces reach it: the Console's pane and, since the 2026-09-15 follow-up, the MCP
	// tool. ADR 0069 kept these out of the tool on the grounds that an agent turning a knob some
	// provider ignores learns nothing — which stopped being true for this one: all seven families
	// read `steps` and `sampler`, and the two a family does not read are named in the result's
	// warnings (comfyIgnoredParamWarnings) rather than swallowed.
	//
	// A POINTER so "the caller said nothing" survives: a zero-valued struct is exactly what the
	// merge reads as "declared nothing", and the two must not be spelled the same at the edge
	// that has to REFUSE a typed value (validateParams) rather than ignore it.
	Params *EngineParams
	// OnPhase is how a long provider call says where it has got to, for the job queue's list
	// (ADR 0081 decision 2). nil on the blocking route, which has nobody to tell.
	//
	// It is called from the provider's own goroutine and may be called more than once with the
	// same phase — a wake seen by the upload and then again by /prompt is two waking events, not
	// a state machine the reader may assume is monotonic.
	OnPhase func(Phase)
	// OnUpstream reports the id the ENGINE gave this request (ComfyUI's prompt_id), which is the
	// only handle a cancel has: Canceller takes it, and without it the alternative is the bare
	// /interrupt that kills another workspace's picture on a shared box.
	OnUpstream func(id string)
}

// Phase is how far along one generation is, in the words the job list shows. It is not a
// progress bar and deliberately cannot become one: per-step progress exists only on ComfyUI's
// websocket and the Control Plane's relay does not upgrade connections (ADR 0081 decision 2).
//
// What it buys is the difference between "the engine is starting, the first picture waits
// several minutes" and an unexplained five-minute `running`.
type Phase string

const (
	PhaseWaking    Phase = "waking"
	PhaseUploading Phase = "uploading"
	PhaseRunning   Phase = "running"
	PhaseFetching  Phase = "fetching"
)

// reportPhase and reportUpstream are the nil-safe callers, so no provider has to write the
// check at each of the four places it reports from.
func (r Request) reportPhase(p Phase) {
	if r.OnPhase != nil {
		r.OnPhase(p)
	}
}

func (r Request) reportUpstream(id string) {
	if r.OnUpstream != nil && strings.TrimSpace(id) != "" {
		r.OnUpstream(id)
	}
}

// Canceller is an OPTIONAL capability a Provider may implement: "a request the engine has
// already accepted can be taken back". Kept off the core Provider interface because most routes
// have no such thing — a vendor call is in flight or it is not, and sdcpp's OpenAI-compatible
// endpoint offers nothing to cancel with — and a required method every implementation answers
// with "not supported" is a method with no information in it (ADR 0081 decision 2).
//
// upstream is whatever the provider reported through Request.OnUpstream. A provider that is
// asked to cancel an id it never issued must refuse rather than cancel "whatever is running":
// the engine box is shared across workspaces.
type Canceller interface {
	Cancel(ctx context.Context, upstream string) error
}

// Build is the Agent's own version, stamped by main at startup. It rides in the sidecar
// (ADR 0081 decision 3) because "which Agent wrote this graph" is the one piece of provenance a
// picture cannot carry any other way — the templates change between releases and the sidecar
// outlives the binary that wrote it.
var Build = "dev"

// LoraRef is one LoRA a request asks for: a name out of Caps.Loras, and how strongly to apply
// it. A LoRA whose base model does not match the chosen checkpoint is refused by the PROVIDER
// while it assembles the request (ADR 0072 decision 5, レビュー決定 5) — the Control Plane never
// sees it, because the pairing lives inside a workflow graph the gateway must not read.
type LoraRef struct {
	// The tags are the sidecar's (ADR 0081 decision 3) and the props route's: a picture's record
	// holds the LoRAs it was made with in the same spelling the request names them by.
	Name string `json:"name"`
	// Weight is 0-2, and 0 means "not stated": a LoRA asked for at strength zero is a LoRA that
	// does nothing, which nobody means, so the provider reads it as decision 5's default of 1.
	Weight float64 `json:"weight,omitempty"`
}

// Image is one produced picture, in memory. A provider hands these back and never decides
// where they live.
type Image struct {
	Bytes  []byte
	MIME   string
	Width  int
	Height int
	// Seed is the sampler noise THIS picture came from — the request's base seed for batch index
	// 0 and base+i after, which is how ComfyUI derives a batch's noise. nil on a route that has
	// no seed to report (ADR 0081 decision 3).
	//
	// It rides per image rather than on Result because a batch of four is four different
	// pictures, and "it was random and I cannot get it back" is the complaint this exists to
	// answer.
	Seed *int64
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
	// Negative is whether Request.NegativePrompt reaches the sampler as real conditioning.
	//
	// A per-MODEL answer, not a per-provider one, which is why it sits in Caps: on the fleet's
	// own ComfyUI it is true for the guided families (SDXL, SD3.5) and FALSE for the distilled
	// ones. Those run at cfg 1, where the guidance term is `uncond + 1*(cond-uncond)` = cond —
	// the negative branch cancels out exactly, so a graph can carry the words and the picture
	// cannot change. Reporting true there would be the most expensive kind of lie: the caller
	// sees no warning, the picture looks fine, and what they asked to exclude is still in it.
	Negative bool
	// Params is whether Request.Params reaches the sampler at all (ADR 0081 decision 4). Only a
	// route that BUILDS the graph has these knobs to turn — the vendor routes have no steps and
	// no sampler name, and sdcpp's command line was fixed when the box started — so a request
	// that carries them anywhere else comes back as a warning rather than being dropped.
	//
	// Per provider rather than per model: which of the four a FAMILY reads differs (flux1 has no
	// cfg), and that is reported per request as a warning, not as a capability the form could
	// read one model at a time.
	Params bool
	// Strength is whether Request.Strength reaches the sampler on an EDIT.
	//
	// Inpaint is excluded even where this is true, and that is not an omission: what preserves
	// the area OUTSIDE an inpaint mask is the noise mask, not a partial denoise. Lowering it
	// there protects nothing and makes the repainted area a weak echo of what it replaced, so
	// the request is reported back in warnings rather than honoured.
	Strength bool
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
	ID string
	// Label is what a member is shown instead of the id (ADR 0090). Empty falls back to the id.
	Label       string
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

// Studio is the MEMBER-facing catalogue of one provider (ADR 0081 decision 5): everything a
// person filling in a generation form has to know, from the same rows the MCP path already
// receives, with no second projection on the Control Plane to keep in step.
//
// It is a different answer from Caps and from ModelInfo, and deliberately so: those exist to
// decide what a TOOL may advertise to a model, where the honest answer is a short one. This is
// what a form renders — placeholders, the fields to grey out, the administrator's own negative
// with its origin shown, the licence the weights came under.
type Studio struct {
	Models []StudioModel
	Loras  []StudioLora
	// Samplers and Schedulers are the allow-lists this Agent will send, so the form cannot offer
	// a name the jobs route would then refuse by name.
	Samplers   []string
	Schedulers []string
	// NegativeAlways is the deployment administrator's own exclusion list, shown as a fixed chip
	// the member cannot remove — it is the admin's, and merging it in invisibly is what ADR 0072
	// already refused to do.
	NegativeAlways string
	// LoraWeightMax is the ceiling one adapter may be asked for at. There is no default weight:
	// no column holds one, and the Agent uses 1 when nobody states one.
	LoraWeightMax float64
}

// StudioModel is one checkpoint as a form needs to see it.
type StudioModel struct {
	ID string
	// Label is what a member is shown instead of the id (ADR 0090). Empty falls back to the id.
	Label       string
	Description string
	// Family is the workflow template (`sdxl`, `flux1`, …). It decides everything below it.
	Family string
	Sizes  []string
	// Ops is this MODEL's own answer, unlike the route's advertised Caps("").Ops (ADR 0094
	// decision 12): a provider's union has to include every op some model can do, or a
	// generate-only checkpoint would vanish from what "auto" can be offered at all (decision 11),
	// but a form that is currently pointed at one specific model needs its own, narrower answer —
	// offering "generate" on an edit-only row is decision 2's 400, in front of the member, every
	// time they press it.
	Ops []Op
	// Params are the EFFECTIVE defaults — the family recipe with the catalogue row laid over it
	// — so the form's placeholders are what will actually run if the member types nothing.
	Params EngineParams
	// Negative is the row's own recommended negative prompt.
	Negative string
	// Knobs is the subset of `steps cfg sampler scheduler negative` this family READS. The form
	// greys out the rest on this word rather than on a table of its own: the two must not be able
	// to disagree, and this Agent is where the templates are.
	Knobs []string
	Warm  bool
	// The licence the weights came under and where they came from, for a member who is about to
	// publish what they make. The catalogue holds all three; they reach the Agent on
	// engineCatalogModelRow.
	LicenseName string
	LicenseURL  string
	SourceURL   string
}

// StudioLora is one fine-tune as the form needs it.
type StudioLora struct {
	Name        string
	Description string
	BaseModel   string
	// TrainedWords are the trigger words the adapter's author published. The single most common
	// "the LoRA does nothing" is a missing trigger, and no amount of prompt help fixes it.
	TrainedWords []string
	// Weight is the strength the catalogue row declares, 0 for "not declared" — in which case the
	// provider uses 1.
	Weight float64
}

// StudioLister is the OPTIONAL capability behind the widened GET /imagegen/status. Only the
// route that builds the graph has any of this to report, so the vendor routes do not implement
// it and their models go out with the short shape they always had.
type StudioLister interface {
	Studio(ctx context.Context) (Studio, bool)
}

func studioOf(ctx context.Context, p Provider) (Studio, bool) {
	sl, ok := p.(StudioLister)
	if !ok {
		return Studio{}, false
	}
	return sl.Studio(ctx)
}

// Provider ids. The id is the wire value the MCP surface and the ledger both carry.
const (
	ProviderCodex = "codex"
	ProviderAgy   = "agy"
	// ProviderOpenAICompat speaks the OpenAI Images API against whatever server an engine table
	// row points it at — this fleet's own GPU (ADR 0071), an operator's LAN box (ADR 0076),
	// another fleet's borrowed engine (ADR 0079), or a metered vendor endpoint. The id names the
	// PROTOCOL, not who runs the box or who pays for it (ADR 0083).
	ProviderOpenAICompat = "openai-compat"
	// ProviderComfy is the fleet's own engine, the ComfyUI alternative (ADR 0072 decision 4,
	// phase P2). Same transport as openai-compat — the Control Plane's engine gateway — but it
	// holds several checkpoints at once and switches per REQUEST, which is what makes `model` a
	// real choice instead of a fixed fact about the deployment. The `image` role this stack buys
	// runs comfy alone (it is the only image server 60-engines.yaml builds); a deployment may
	// still declare further rows under other providers alongside it (ADR 0082), openai-compat
	// ones included.
	ProviderComfy = "comfy"
)

// providerOrder is the BUILT-IN order "auto" walks. The first two entries are Tier-1 (ADR 0069
// decision 3): each runs on a login the container already holds, and neither costs a new secret
// or an egress allowlist entry. The third is the fleet's own hardware (ADR 0071), present only
// in a deployment that stood an image engine up.
//
// The fleet's own image providers are first where they exist, and the reason is whose account
// pays: the other two spend a MEMBER's plan quota — invisibly, three to five times faster than
// a text turn, which is why the whole feature is off by default (decision 8) — while a
// deployment that stood up an image engine, or pointed a row at one, has already decided to pay
// for it itself. They also honour more of the request than either: the sizes a row actually
// declares, plus edit and inpaint, which neither of the others can do at all. They are simply
// absent from `Ready` where no such row exists, which is most deployments, so this does not
// change what anyone gets today.
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
	{ID: ProviderOpenAICompat, Fleet: true},
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

// providerIsFleet answers whether the fleet's own hardware — or another fleet's, borrowed under
// ADR 0079 decision 9 — serves this provider id. Unknown ids are NOT fleet: an id nobody declared
// is not something this deployment can be said to pay for.
//
// Every currently declared images row is fleet, full stop (ADR 0082 decision 4), checked BEFORE
// the static list: getting this backwards reproduces a measured accident (ADR 0072, 2026-09-11)
// where a provider this build did not have a static entry for was filed as external and inserted
// BEHIND a member's own plan, so "auto" spent that plan before it ever reached hardware the
// deployment was already paying for.
func providerIsFleet(id string) bool {
	for _, row := range imageProviderRowsSafe() {
		if row.Key == id {
			return true
		}
	}
	for _, r := range providerRanks {
		if r.ID == id {
			return r.Fleet
		}
	}
	return false
}

// imageProviderRowsSafe reads EngineImageRows if the Agent installed it, deduplicated by key: a
// malformed catalogue naming the same key twice must not hand Run() two Provider instances that
// both answer to the same id. nil when the deployment has no engines at all, or when
// EngineImageRows itself is nil (a dev Agent with no Control Plane).
func imageProviderRowsSafe() []EngineImageRow {
	if EngineImageRows == nil {
		return nil
	}
	rows := EngineImageRows(context.Background())
	seen := map[string]bool{}
	out := make([]EngineImageRow, 0, len(rows))
	for _, r := range rows {
		if r.Key == "" || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		out = append(out, r)
	}
	return out
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
	rows := imageProviderRowsSafe()
	order := dynamicOrder(rows)
	known := map[string]bool{}
	for _, id := range order {
		known[id] = true
	}
	seen := map[string]bool{}
	var chosen []string
	if ProviderOrderPref != nil {
		for _, id := range normalizeStoredProviderOrder(ProviderOrderPref(), rows) {
			if known[id] && !seen[id] {
				seen[id] = true
				chosen = append(chosen, id)
			}
		}
	}
	// The unmentioned ones, split by who pays and each half kept in the built-in order.
	var fleet, external []string
	for _, id := range order {
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
	out := make([]string, 0, len(order))
	out = append(out, fleet...)
	out = append(out, chosen...)
	return append(out, external...)
}

// dynamicOrder is providerOrder — the built-in, test-overridable base — with every currently
// declared images row taking the SLOT its kind held (ADR 0082 decision 1), not appended after
// it: a bare kind name in the base list ("comfy", "openai-compat") stands for "wherever this
// deployment's engine of that kind is", and once a real row of that kind exists, Providers() no
// longer constructs anything answering to the bare name. Leaving the placeholder in as well as
// the row would give the order TWO entries for the one thing this deployment actually runs — a
// phantom nothing can ever be ready under, sitting in front of the row it was standing in for.
//
// A deployment with no dynamic rows at all behaves exactly as it always did: every base id
// passes through unchanged.
func dynamicOrder(rows []EngineImageRow) []string {
	byKind := map[string][]string{}
	for _, row := range rows {
		byKind[row.Provider] = append(byKind[row.Provider], row.Key)
	}
	var out []string
	seen := map[string]bool{}
	push := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range providerOrder {
		if keys, ok := byKind[id]; ok {
			for _, k := range keys {
				push(k)
			}
			continue
		}
		push(id)
	}
	// Any row whose kind has no slot in the built-in base at all (there is none today — comfy
	// and openai-compat both do — but EngineImageRow.Provider is free text, and a future third
	// kind must not be silently dropped just because it has no legacy placeholder to fill).
	for _, row := range rows {
		push(row.Key)
	}
	return out
}

// normalizeStoredProviderOrder expands a legacy PROVIDER-KIND alias — "comfy" or "openai-compat",
// the only ids a preference saved before ADR 0082 could ever have named — into every images row of
// that kind this deployment currently declares, in catalogue order (ADR 0082 decision 3).
//
// Dropping the alias instead of expanding it reproduces the accident normalizeImageProviderOrder
// was written to prevent (ADR 0072, 2026-09-11): a fleet engine whose row key is not literally the
// kind name would fall out of a stored order entirely and come back through the "unmentioned"
// path below, which only agrees with what the user actually ranked when there is exactly one
// fleet row to confuse it with.
func normalizeStoredProviderOrder(pref []string, rows []EngineImageRow) []string {
	byKind := map[string][]string{}
	for _, row := range rows {
		byKind[row.Provider] = append(byKind[row.Provider], row.Key)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(pref))
	push := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range pref {
		if id == ProviderComfy || id == ProviderOpenAICompat {
			for _, key := range byKind[id] {
				push(key)
			}
			continue
		}
		push(id)
	}
	return out
}

// Providers returns the registered providers: the vendor routes (ADR 0069 decision 3), whose id
// never changes, plus one provider per images row this deployment currently declares (ADR 0082
// decision 1) — the row's OWN key becomes that provider's id, and the row's declared Provider
// field picks which client implementation serves it. Built fresh on each call so a changed
// environment (a Codex login that arrived after boot, an engine table row added since) is picked
// up, and a var so a test can drive Run without a Codex CLI on PATH.
var Providers = func() []Provider {
	out := []Provider{newCodexProvider(), newAgyProvider()}
	for _, row := range imageProviderRowsSafe() {
		switch row.Provider {
		case ProviderComfy:
			out = append(out, newComfyProviderFor(row.Key))
		case ProviderOpenAICompat:
			out = append(out, newOpenAICompatProviderFor(row.Key))
		}
	}
	return out
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

// modelOwner is ADR 0094 decision 13's answer to "who actually knows this model": the first
// provider in order whose ModelLister.Models() lists it BY ID. false when no provider claims it
// (an unknown id, or a name typed for a provider that has no model list at all — codex and agy
// implement no ModelLister, so a model argument aimed at either never matches here and this
// function correctly says nothing owns it, leaving their existing model-in-the-prompt routes
// untouched).
func modelOwner(ctx context.Context, provs map[string]Provider, order []string, model string) (string, bool) {
	for _, id := range order {
		p, ok := provs[id]
		if !ok {
			continue
		}
		ml, ok := p.(ModelLister)
		if !ok {
			continue
		}
		for _, m := range ml.Models(ctx) {
			if m.ID == model {
				return id, true
			}
		}
	}
	return "", false
}

// joinOps spells a Caps.Ops list for a refusal message.
func joinOps(ops []Op) string {
	out := make([]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, string(o))
	}
	return strings.Join(out, ", ")
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
	// Seed is this picture's own sampler seed — see Image.Seed. A POINTER on the wire too: 0 is
	// a perfectly good seed, and omitting it when it is 0 would make the one route that CAN
	// reproduce a picture look like one that cannot.
	Seed *int64 `json:"seed,omitempty"`
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
	order := effectiveOrder()
	// ADR 0094 decision 13: a NAMED model, on an otherwise-auto request, pins routing to the
	// provider that actually knows it — a provider whose Caps ignores the model argument
	// entirely (codex, agy: neither implements ModelLister) would otherwise keep answering
	// Supports(req.Op)=true after the one provider that DOES look at the model already refused
	// it, and the request would be quietly generated by a route that never heard of the model
	// name, on a member's own plan (measured: `model=qwen-image-edit-2509`, `op=generate`, no
	// provider named — comfy's own Caps(model) refuses on the strict per-model answer, and
	// nothing stopped auto from falling through to agy's model-blind Caps("")=true). A `pref`
	// is unaffected — chooseImageProviders already pins to it before this runs.
	if pref := strings.TrimSpace(job.Pref); (pref == "" || pref == "auto") && req.Model != "" {
		if owner, ok := modelOwner(ctx, provs, order, req.Model); ok {
			if !capsOf(owner).Supports(req.Op) {
				return Stored{}, fmt.Errorf("%w: model %s cannot do %s (it can: %s)",
					ErrNoProvider, req.Model, req.Op, joinOps(capsOf(owner).Ops))
			}
			order = []string{owner}
		}
	}
	candidates := chooseImageProviders(job.Pref, req, order, ready, capsOf)
	if len(candidates) == 0 {
		return Stored{}, ErrNoProvider
	}

	var attempts []attemptFailure
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
		recordUsage(ctx, job.Session, name, res, err == nil && len(res.Images) > 0, started)
		if err == nil && len(res.Images) == 0 {
			err = errors.New("the provider returned no image")
		}
		if err != nil {
			attempts = append(attempts, attemptFailure{id: name, err: err})
			continue // the next provider in the order, if the caller left the choice to us
		}

		files, err := storeImages(job.SID, res.Images)
		if err != nil {
			// A storage failure is OURS, not the provider's: the picture exists and trying a
			// second provider would spend more quota to hit the same broken disk.
			return Stored{}, err
		}
		warnings := append(fallbackWarnings(name, attempts), res.Warnings...)
		// res.Model, not req.Model: a provider may resolve a request naming no model to a
		// DIFFERENT row than its own warm default (ADR 0094 decision 11's comfyFirstModelForOp,
		// when the warm row does not offer the requested op), so Caps has to answer for the row
		// that actually ran. Asking with req.Model's empty string would hit decision 11's own
		// union instead — the right answer for deciding what to ADVERTISE, and the wrong one for
		// reporting what THIS request's warnings actually are.
		return Stored{
			Files:       files,
			Provider:    res.Provider,
			Model:       res.Model,
			Region:      res.Region,
			Destination: res.Destination,
			Warnings:    append(warnings, requestWarnings(req, res, p.Caps(res.Model))...),
			CostUSD:     res.CostUSD,
		}, nil
	}
	// Every candidate failed. Report them all: "codex is out of quota, and the local engine is
	// not running" is actionable in a way that either half alone is not.
	errs := make([]error, len(attempts))
	for i, a := range attempts {
		errs[i] = a
	}
	return Stored{}, errors.Join(errs...)
}

// attemptFailure is one candidate Run() tried and failed: which id, why, carried as a value
// (rather than a pre-formatted error) because fallbackWarnings needs the bare id back to ask
// providerIsFleet about it — decision 5 below reads differently depending on the answer.
type attemptFailure struct {
	id  string
	err error
}

func (a attemptFailure) Error() string { return fmt.Sprintf("%s: %v", a.id, a.err) }
func (a attemptFailure) Unwrap() error { return a.err }

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
//
// ADR 0082 decision 5: with two fleet rows, a fall-through CAN land entirely inside this
// deployment's own hardware — the LAN engine was down, the borrowed one answered instead — and
// nobody's plan quota moved. Saying "a different account's plan" there would be exactly the lie
// this function exists to prevent, just spelled the other way round: the two routes differ in
// WHO PAYS, not in which account, so that is the question the wording is chosen on.
func fallbackWarnings(used string, attempts []attemptFailure) []string {
	if len(attempts) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(attempts))
	sameWallet := providerIsFleet(used)
	for _, a := range attempts {
		reasons = append(reasons, a.Error())
		if !providerIsFleet(a.id) {
			sameWallet = false
		}
	}
	if sameWallet {
		return []string{fmt.Sprintf(
			"fell back to %s because the provider(s) ahead of it failed: %s — this deployment's own engine answered instead, no member's plan was spent",
			used, strings.Join(reasons, "; "))}
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
	r.NegativePrompt = strings.TrimSpace(r.NegativePrompt)
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
// 🔴 `caps` is the row that ACTUALLY RAN (`Caps(res.Model)`), never the one the request named.
// With ADR 0094 decision 11, `Caps("")` is a union over the enabled models, so a request that named
// no model would be judged against "some row here can do it" and every per-model warning below
// would fall silent — measured on `strength` before it was per-model's turn (ADR 0094 decision 2).
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
	// Same shape again, and the reason it matters more than the others: what a negative prompt
	// asks to keep out is usually the thing the caller is trying to avoid SHOWING someone. A
	// picture that still contains it looks like a success from every angle except that one.
	if req.NegativePrompt != "" && !caps.Negative {
		out = append(out, fmt.Sprintf(
			"negative_prompt=%q requested, but this route has no negative conditioning — nothing was excluded",
			req.NegativePrompt))
	}
	// Same shape once more, and invisible in the same way a dropped LoRA is: a picture sampled at
	// the route's own 20 steps when 50 were asked for looks like a picture, and only the person
	// who asked knows it is not the one they asked for.
	if req.Params != nil && *req.Params != (EngineParams{}) && !caps.Params {
		out = append(out, "steps / cfg / sampler / scheduler were requested, but this route does not build"+
			" the sampler graph — it ran at its own fixed settings")
	}
	// A dropped seed is the most invisible of the three: the picture is fine, and the caller only
	// finds out when the SECOND request — the whole point of pinning one — comes back different.
	if req.Seed != nil && !caps.Seed {
		out = append(out, fmt.Sprintf(
			"seed=%d requested, but this route cannot pin a seed — two calls with the same seed will not match", *req.Seed))
	}
	// Invisible in the same way, and with one fewer chance of being noticed than the seed: the
	// picture is a perfectly good edit, and the only thing wrong with it is HOW MUCH of the input
	// survived — which nobody can see without the version they asked for to hold it against.
	if req.Strength != nil {
		switch {
		case req.Op != OpEdit:
			out = append(out, fmt.Sprintf(
				"strength=%g requested, but only op=edit starts from your picture — %s ran a full denoise and ignored it",
				*req.Strength, req.Op))
		case !caps.Strength:
			out = append(out, fmt.Sprintf(
				"strength=%g requested, but this route cannot vary how much of the input it keeps — it edited at its own fixed amount",
				*req.Strength))
		}
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
//
// ref is the session the picture was made for, and it is EMPTY for the Console's job queue
// (ADR 0081): that path has no session, and naming one that does not exist would file a
// member's own volume under a conversation nobody had.
func recordUsage(ctx context.Context, ref string, chosen string, res Result, ok bool, started time.Time) {
	tag := usagex.Tag{Feature: usagex.FeatureToolImagegen, Trigger: usagex.TriggerUser, Ref: ref}
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
