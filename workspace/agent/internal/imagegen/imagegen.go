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
	Sizes       []string
	Backgrounds []string
	MaxCount    int
	MaxInputs   int
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

// Provider ids. The id is the wire value the MCP surface and the ledger both carry.
const (
	ProviderCodex = "codex"
	ProviderSdcpp = "sdcpp"
)

// providerOrder is what "auto" walks, best-supported first.
//
// sdcpp before codex, and the reason is whose account pays. The Codex route spends the
// USER's ChatGPT plan quota — invisibly, three to five times faster than a text turn, which
// is why the whole feature is off by default (ADR 0069 decision 8). A deployment that stands
// up the image engine has already decided to pay for that hardware itself, and it also gets
// edit and inpaint, which the Codex route cannot do at all. Naming `codex` explicitly still
// picks it: an explicit choice is honoured even when auto would not have made it.
var providerOrder = []string{ProviderSdcpp, ProviderCodex}

// Providers returns the registered providers, in providerOrder. Built fresh on each call so a
// changed environment (a Codex login that arrived after boot, an engine stack deployed since)
// is picked up, and a var so a test can drive Run without a Codex CLI on PATH.
var Providers = func() []Provider {
	return []Provider{newSdcppProvider(), newCodexProvider()}
}

// modelNamer is implemented by a provider that can name its default model WITHOUT calling
// anything — which for a self-hosted engine is the whole trick, since the engine is asleep
// when the question is asked.
type modelNamer interface{ DefaultModel() string }

// chooseImageProvider decides what "auto" (the default) routes to — the same shape as
// chooseTTSProvider in control-plane/tts.go.
//
// An explicit choice is honoured as-is EVEN WHEN IT IS NOT READY: the caller named a
// provider, and that provider's own error ("codex is not logged in") is a better answer than
// silently producing an image on a different service, billed to a different account. Only
// auto is allowed to walk past an unready one. "" means nothing can serve the request.
func chooseImageProvider(pref string, req Request, order []string, ready map[string]bool, caps func(id string) Caps) string {
	if pref != "" && pref != "auto" {
		return pref
	}
	for _, id := range order {
		if ready[id] && caps(id).Supports(req.Op) {
			return id
		}
	}
	return ""
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
	name := chooseImageProvider(job.Pref, req, providerOrder, ready, capsOf)
	if name == "" {
		return Stored{}, ErrNoProvider
	}
	p, ok := provs[name]
	if !ok {
		return Stored{}, fmt.Errorf("%w: %s", ErrUnknownProvider, name)
	}
	if !p.Caps(req.Model).Supports(req.Op) {
		return Stored{}, fmt.Errorf("%w: %s cannot do %s", ErrNoProvider, name, req.Op)
	}

	started := time.Now()
	res, err := p.Generate(ctx, req)
	// Record on every path, including the failed one: a turn that burned driver tokens and
	// produced nothing still consumed the user's plan, and a row with ok:false is what keeps
	// that visible (ADR 0029 §3).
	recordUsage(ctx, job, res, err == nil, started)
	if err != nil {
		return Stored{}, err
	}
	if len(res.Images) == 0 {
		return Stored{}, errors.New("the provider returned no image")
	}
	files, err := storeImages(job.SID, res.Images)
	if err != nil {
		return Stored{}, err
	}
	return Stored{
		Files:       files,
		Provider:    res.Provider,
		Model:       res.Model,
		Region:      res.Region,
		Destination: res.Destination,
		Warnings:    append(res.Warnings, requestWarnings(req, res)...),
		CostUSD:     res.CostUSD,
	}, nil
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
	r.Background = strings.TrimSpace(r.Background)
	return r
}

// requestWarnings is the core's own comparison of what was asked for against what arrived.
// The provider reports what IT knows it could not honour; this catches the rest — most
// importantly a count that came back short, which no provider can see as a failure because
// each image it did produce is fine.
func requestWarnings(req Request, res Result) []string {
	var out []string
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
func recordUsage(ctx context.Context, job Job, res Result, ok bool, started time.Time) {
	tag := usagex.Tag{Feature: usagex.FeatureToolImagegen, Trigger: usagex.TriggerUser, Ref: job.Session}
	call := usagex.Call{
		Kind:     usageKindOf(res.Provider, job.Pref),
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
func usageKindOf(provider, pref string) string {
	if provider == "" {
		provider = pref
	}
	if provider == ProviderCodex {
		return session.KindCodex
	}
	return provider
}
