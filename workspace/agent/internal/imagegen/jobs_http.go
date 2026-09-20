package imagegen

// The queue's REST face (ADR 0081 decision 2 and 12). Unlike POST /imagegen/generate these ARE
// on the Control Plane's agent-proxy list: the Console is their only caller.
//
// 🔴 The `agents.image_generation` opt-in does not gate them. That preference exists because the
// MCP tool spends a MEMBER's own ChatGPT or Antigravity plan quota from sessions that are not
// that CLI's, invisibly (ADR 0069 decision 8). This path reaches only the engines the deployment
// runs itself, at nobody's personal expense, and fleetProviderFor refuses anything else — so
// gating it here would switch off the one route the preference was never about.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// jobRequest is POST /imagegen/jobs' body: the existing generateRequest WITHOUT `session` (this
// path has none — the folder is decision 3's and the "not the session's own CLI" check has
// nothing to check), plus the pane's own fields.
//
// The inherited keys keep their existing camelCase spelling and the new ones are snake_case,
// which is how ADR 0081 writes them. Renaming the old ones would be a wire break for the MCP
// route that shares the shape, for a consistency nothing reads.
type jobRequest struct {
	Provider       string        `json:"provider"`
	Op             string        `json:"op"`
	Prompt         string        `json:"prompt"`
	NegativePrompt string        `json:"negativePrompt"`
	Size           string        `json:"size"`
	AspectRatio    string        `json:"aspectRatio"`
	Background     string        `json:"background"`
	Count          int           `json:"count"`
	Inputs         []string      `json:"inputs"`
	Mask           string        `json:"mask"`
	Model          string        `json:"model"`
	Loras          []loraRequest `json:"loras"`
	Seed           *int64        `json:"seed"`
	Strength       *float64      `json:"strength"`
	// Params is the sampler overlay (decision 4). A pointer, so an absent key and a zeroed object
	// stay apart.
	Params *EngineParams `json:"params"`
	// Label is free text shown on the group and on every job of it.
	Label string `json:"label"`
	// OutDir is a browse-root-relative folder for keepers, created on first use. It exists for
	// sorting, not for survival.
	OutDir string `json:"out_dir"`
	// Jobs is N: N jobs of one picture each (decision 8), NOT a batch of N.
	Jobs       int    `json:"jobs"`
	SeedPolicy string `json:"seed_policy"`
	// Trial marks decision 11's quick preview: one picture, at the head of the queue, at the
	// family's trial step count, into generated/console/trial/.
	Trial bool `json:"trial"`
	// FullSteps turns the step reduction off, for when the trial IS the picture.
	FullSteps bool `json:"full_steps"`
}

// HandleJobs answers POST and GET /imagegen/jobs.
func HandleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, jobs.List())
		return
	}
	var body jobRequest
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	spec, errCode, errMsg := body.spec()
	if errCode != "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	out, err := jobs.Enqueue(r.Context(), spec)
	if err != nil {
		writeEnqueueErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func writeEnqueueErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errQueueFull):
		httpx.WriteErr(w, http.StatusTooManyRequests, "queue_full", err.Error())
	case errors.Is(err, errTrialPending):
		httpx.WriteErr(w, http.StatusTooManyRequests, "trial_pending", err.Error())
	case errors.Is(err, ErrNoProvider):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "imagegen_no_provider", err.Error())
	case errors.Is(err, ErrUnknownProvider):
		httpx.WriteErr(w, http.StatusBadRequest, "imagegen_unknown_provider", err.Error())
	case errors.Is(err, errNoBrowseRoot):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "no_browse_root", err.Error())
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// spec validates the body and turns it into a JobSpec. The code it returns is the wire's error
// code, so the Console shows one message per kind of refusal rather than one for all of them.
func (b jobRequest) spec() (JobSpec, string, string) {
	if strings.TrimSpace(b.Prompt) == "" {
		return JobSpec{}, "bad_prompt", "prompt is required"
	}
	op := Op(strings.TrimSpace(b.Op))
	if op == "" {
		op = OpGenerate
	}
	if !ValidOp(op) {
		return JobSpec{}, "bad_op", "unknown op: " + string(op)
	}
	if s := b.Strength; s != nil && (*s <= 0 || *s > 1) {
		return JobSpec{}, "bad_strength", fmt.Sprintf(
			"strength must be greater than 0 and at most 1 (got %g): 1 redraws the picture from the prompt alone,"+
				" and small values keep more of the input", *s)
	}
	// ADR 0094 decision 2/4, same shape as HandleGenerate's: refused BY VALUE when the resolved
	// family does not read strength or size at all, not left to a family that quietly ignores it.
	// A different code from the value-range refusal above — see HandleGenerate's own note.
	if b.Strength != nil {
		if msg := comfyStrengthRefusal(b.Provider, b.Model, string(op)); msg != "" {
			return JobSpec{}, "bad_strength_family", msg
		}
	}
	if msg := comfySizeRefusal(b.Provider, b.Model, string(op), b.Size); msg != "" {
		return JobSpec{}, "bad_size_family", msg
	}
	if b.Count < 0 || b.Count > comfyMaxBatch {
		return JobSpec{}, "bad_count", fmt.Sprintf(
			"count is the ComfyUI batch size and this route allows at most %d (got %d) — to make more pictures,"+
				" ask for more JOBS, which can be cancelled one at a time", comfyMaxBatch, b.Count)
	}
	if b.Jobs < 0 || b.Jobs > imagegenQueueMax {
		return JobSpec{}, "bad_jobs", fmt.Sprintf("jobs must be between 1 and %d (got %d)", imagegenQueueMax, b.Jobs)
	}
	if !validSeedPolicy(b.SeedPolicy) {
		return JobSpec{}, "bad_seed_policy", fmt.Sprintf("unknown seed_policy %q: it is one of %s, %s or %s",
			b.SeedPolicy, SeedRandom, SeedFixed, SeedSequence)
	}
	if err := validateRequestParams(b.Params); err != nil {
		return JobSpec{}, "bad_params", err.Error()
	}
	if err := validateRequestSize(b.Size); err != nil {
		return JobSpec{}, "bad_params", err.Error()
	}
	loras := make([]LoraRef, 0, len(b.Loras))
	for _, l := range b.Loras {
		loras = append(loras, LoraRef{Name: l.Name, Weight: l.Weight})
	}
	return JobSpec{
		Provider: b.Provider,
		Request: Request{
			Op: op, Prompt: b.Prompt, NegativePrompt: b.NegativePrompt,
			Size: b.Size, AspectRatio: b.AspectRatio, Background: b.Background,
			Count: b.Count, Inputs: b.Inputs, Mask: b.Mask, Model: b.Model,
			Loras: loras, Seed: b.Seed, Strength: b.Strength, Params: b.Params,
		},
		Label: b.Label, OutDir: b.OutDir, Jobs: b.Jobs, SeedPolicy: b.SeedPolicy,
		Trial: b.Trial, FullSteps: b.FullSteps,
	}, "", ""
}

// The bounds on a typed sampler value (ADR 0081 decision 4).
const (
	// paramsMaxSteps is high enough for anyone's "slow but careful" and low enough that a typo
	// cannot buy an hour of a shared GPU with one keystroke.
	paramsMaxSteps = 150
	// paramsMaxCFG is past every published recommendation for these families; above it the
	// picture is burned rather than more faithful.
	paramsMaxCFG = 30
	// sizeMultiple is the latent stride every one of the families is built on — including anima,
	// whose Qwen-Image VAE downscales by 8 like the rest despite carrying 16 channels. A width that
	// is not a multiple of it is silently rounded inside ComfyUI, so what comes back is not the
	// size that was asked for and nothing says so.
	sizeMultiple = 8
)

// validateRequestParams refuses a typed value LOUDLY.
//
// 🔴 This is deliberately not how the catalogue overlay behaves. comfyRecipe.with IGNORES a
// sampler name it does not know and keeps the family's own, because that name may simply be
// newer than this binary and an administrator's old row should not cost every request a
// picture. A member's form is the opposite case: they typed it, they are looking at the result,
// and a value that silently does nothing is indistinguishable from a broken feature. Leniency
// for the row, refusal for the request.
func validateRequestParams(p *EngineParams) error {
	if p == nil {
		return nil
	}
	if s := strings.TrimSpace(p.Sampler); s != "" && !comfyKnownSampler(s) {
		return fmt.Errorf("unknown sampler %q — this Agent sends only %s",
			s, strings.Join(comfySortedNames(comfySamplerNames), ", "))
	}
	if s := strings.TrimSpace(p.Scheduler); s != "" && !comfyKnownScheduler(s) {
		return fmt.Errorf("unknown scheduler %q — this Agent sends only %s",
			s, strings.Join(comfySortedNames(comfySchedulerNames), ", "))
	}
	if p.Steps < 0 || p.Steps > paramsMaxSteps {
		return fmt.Errorf("steps must be between 1 and %d (got %d)", paramsMaxSteps, p.Steps)
	}
	if p.CFG < 0 || p.CFG > paramsMaxCFG {
		return fmt.Errorf("cfg must be between 0 and %d (got %g)", paramsMaxCFG, p.CFG)
	}
	return nil
}

// validateRequestSize refuses a size the box would answer badly or not at all. The pixel ceiling
// is the expensive one: a 2048² SDXL request on an l4 is an out-of-memory five minutes into a
// cold start, and the engine's own 400 comes back bare — not even as engine_waking — so nothing
// downstream can explain it.
func validateRequestSize(size string) error {
	size = strings.TrimSpace(size)
	if size == "" || size == "auto" {
		return nil
	}
	w, h, ok := parseSize(size)
	if !ok {
		return fmt.Errorf("size %q is not <width>x<height>", size)
	}
	if w%sizeMultiple != 0 || h%sizeMultiple != 0 {
		return fmt.Errorf("size %s: each side has to be a multiple of %d, which is the latent stride"+
			" every one of these checkpoint families samples on", size, sizeMultiple)
	}
	if w*h > imagegenMaxPixels {
		return fmt.Errorf("size %s is %d pixels, over this route's ceiling of %d — a picture this large"+
			" runs the engine out of memory after the cold start rather than during it", size, w*h, imagegenMaxPixels)
	}
	return nil
}

// HandleJobCancel answers DELETE /imagegen/jobs/{id}.
func HandleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_id", "job id is required")
		return
	}
	switch err := jobs.Cancel(id); {
	case errors.Is(err, errNoSuchJob):
		httpx.WriteErr(w, http.StatusNotFound, "no_job", "no such job: "+id)
	case err != nil:
		// The job IS cancelled here — it is marked and its result will be discarded — and only
		// the engine's own take-back failed. Saying so is what keeps the pane from showing a
		// picture that is still being made as if nothing had happened.
		httpx.WriteErr(w, http.StatusBadGateway, "cancel_failed", err.Error())
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

type opRequest struct {
	Op string `json:"op"`
}

// HandleGroupOp answers POST /imagegen/groups/{id} — pause, resume, skip, cancel (decision 12).
func HandleGroupOp(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body opRequest
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	switch err := jobs.GroupOp(id, strings.TrimSpace(body.Op)); {
	case errors.Is(err, errNoSuchGroup):
		httpx.WriteErr(w, http.StatusNotFound, "no_group", "no such group: "+id)
	case err != nil:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", err.Error())
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// HandleQueueOp answers POST /imagegen/queue — the same pause and resume over every group at
// once. Trials still run while the queue is paused; that is the reason to pause it.
func HandleQueueOp(w http.ResponseWriter, r *http.Request) {
	var body opRequest
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if err := jobs.QueueOp(strings.TrimSpace(body.Op)); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
