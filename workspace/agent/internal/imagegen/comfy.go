package imagegen

// comfy — the fleet's own image engine (ComfyUI) as an imagegen provider (ADR 0072 decision 4,
// phase P2).
//
// Unlike sdcpp, this is not a one-request-in, one-answer-out OpenAI-compatible call: ComfyUI's
// native API is async by design (POST /prompt returns a queue id at once; the picture is fetched
// once /history/<id> reports it done, and the bytes come from a THIRD call, GET /view). That
// shape is exactly why decision 4 makes ComfyUI the image role's long-term answer over sd-server
// — a synchronous /v1/images/generations request that takes longer than the ingress's 60-second
// idle timeout is unservable (the "60-second規則"), while three short round trips never sit on
// one open connection long enough to hit it, however long the generation between them takes.
//
// The engine gateway (control-plane/engine_gateway.go's dial) still holds the FIRST of those
// three calls while a stopped engine wakes, the same way it holds sdcpp's single call — so the
// retry-on-503-engine_waking loop below is a straight port of sdcpp.go's send(), reused rather
// than reinvented. Only /prompt can hit a cold engine; by the time it answers, ComfyUI is up, so
// the poll and view calls that follow do not repeat the wake dance.
//
// Every checkpoint switch — including the very first request against a just-started engine — is
// EBS-read time on top of generation (measured 1-2.5 minutes, ADR 0072 "実測で解けた点" 5), which
// is why this file is careful to warn about it (comfySwitchWarning) rather than let a caller read
// a slow answer as a broken one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type comfyProvider struct {
	lookup func(ctx context.Context) (EngineConn, bool)
	client *http.Client
}

func newComfyProvider() *comfyProvider {
	return &comfyProvider{lookup: engineLookupFor(ProviderComfy), client: sdcppClient}
}

func (p *comfyProvider) ID() string { return ProviderComfy }

// Ready follows sdcpp's own rule exactly: "this deployment has this engine and we hold a token
// for it", never "the engine is up". See sdcppProvider.Ready for why that is the honest answer.
func (p *comfyProvider) Ready(ctx context.Context) bool {
	_, ok := p.conn(ctx)
	return ok
}

func (p *comfyProvider) conn(ctx context.Context) (EngineConn, bool) {
	if p.lookup == nil {
		return EngineConn{}, false
	}
	c, ok := p.lookup(ctx)
	if !ok || strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Token) == "" {
		return EngineConn{}, false
	}
	return c, true
}

// DefaultModel is what a request naming no model gets (ADR 0072 decision 7): whatever the
// Control Plane last saw this engine actually answer with — free, because it is already loaded
// — falling back to the catalogue's first declared model only when nothing is known to be warm
// yet (a just-started engine, or a CP that restarted and lost its in-memory state).
func (p *comfyProvider) DefaultModel() string {
	c, ok := p.conn(context.Background())
	if !ok {
		return ""
	}
	if c.Warm != "" {
		return c.Warm
	}
	if len(c.Models) > 0 {
		return c.Models[0]
	}
	return ""
}

// Caps is per (provider, model) as everywhere else in this package (ADR 0069 decision 5) — but
// here the model argument is the point of the whole phase: comfy is the first provider in this
// package for which Caps genuinely differs across MULTIPLE models on the same running engine,
// because switching which one answers is exactly what decision 4 buys.
//
// Ops is generate only. edit/inpaint need a per-family image-to-image graph (LoadImage +
// VAEEncode/VAEEncodeForInpaint feeding the same sampler at denoise<1), which nobody has
// measured working on this engine yet — a P2 scope decision, not an oversight; sdcpp still
// offers both.
func (p *comfyProvider) Caps(model string) Caps {
	conn, _ := p.conn(context.Background())
	if strings.TrimSpace(model) == "" {
		model = p.DefaultModel()
	}
	return Caps{
		Ops:       []Op{OpGenerate},
		Sizes:     comfySizesFor(conn, model),
		MaxInputs: 0,
		MaxCount:  4,
	}
}

// Models implements ModelLister (ADR 0072 decision 5, phase P2): every checkpoint the catalogue
// currently enables for this engine, in the same order EngineConn.Models declares them (the
// selected/default one first — see engineImageModelIDs in the Agent's engines.go), each marked
// warm when it is the one decision 7's warm_model names.
func (p *comfyProvider) Models(ctx context.Context) []ModelInfo {
	conn, ok := p.conn(ctx)
	if !ok {
		return nil
	}
	out := make([]ModelInfo, 0, len(conn.Models))
	for _, id := range conn.Models {
		out = append(out, ModelInfo{ID: id, Description: conn.Descriptions[id], Warm: id != "" && id == conn.Warm})
	}
	return out
}

// comfySizesFor prefers the catalogue's own declaration (ADR 0072 decision 2) and falls back to
// the one size every family in this template set was actually trained and measured at.
func comfySizesFor(conn EngineConn, model string) []string {
	if s := conn.Sizes[model]; len(s) > 0 {
		return s
	}
	return []string{"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"}
}

// comfyFamilyFor reads the catalogue's declared family for a model id. False when undeclared or
// unrecognised — comfy refuses rather than guessing a family from the id string the way sdcpp's
// legacy fallback does, because decision 2 exists precisely so a family is a declared fact, not
// something read off a naming convention that will eventually collide.
func comfyFamilyFor(conn EngineConn, model string) (comfyFamily, bool) {
	switch f := comfyFamily(strings.TrimSpace(conn.BaseModel[model])); f {
	case ComfyFamilySDXL, ComfyFamilySD35, ComfyFamilyFlux1, ComfyFamilyFlux2Klein, ComfyFamilyZImage:
		return f, true
	default:
		return "", false
	}
}

func errUnknownComfyFamily(family comfyFamily) error {
	return fmt.Errorf("no workflow template for checkpoint family %q", string(family))
}

func errComfyMissingFile(family, role string) error {
	return fmt.Errorf("the %s checkpoint's catalogue entry has no %s file declared", family, role)
}

// comfySwitchWarning says out loud when a request is about to pay the checkpoint-switch cost
// (ADR 0072 "実測で解けた点" 5: 1-2.5 minutes of EBS re-read, on top of the actual generation) —
// a caller who asked for a cold checkpoint and waited two extra minutes deserves to be told WHY,
// the same reasoning that makes sdcpp's retry loop report `lastWaking` rather than staying mute.
// A heuristic, not a guarantee (another request could have changed what is warm in between), and
// silent when nothing is known to be warm at all — a just-started engine pays this cost on
// EVERY first request regardless of which model is asked for, so naming one as "the switch"
// would blame the wrong thing.
func comfySwitchWarning(conn EngineConn, model string) string {
	if conn.Warm == "" || conn.Warm == model {
		return ""
	}
	return fmt.Sprintf(
		"switching the engine's checkpoint from %s to %s — this can take 1-2.5 minutes (EBS re-read), not a stall",
		conn.Warm, model)
}

func (p *comfyProvider) Generate(ctx context.Context, req Request) (Result, error) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Result{}, errors.New("this deployment runs no self-hosted image engine")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.DefaultModel()
	}
	if model == "" {
		return Result{}, errors.New("no enabled checkpoint in the catalogue")
	}
	caps := p.Caps(model)
	if !caps.Supports(req.Op) {
		return Result{}, fmt.Errorf("the self-hosted image engine cannot do %s", req.Op)
	}
	if req.Prompt == "" {
		return Result{}, errors.New("a prompt is required")
	}
	family, ok := comfyFamilyFor(conn, model)
	if !ok {
		return Result{}, fmt.Errorf("model %s has no declared checkpoint family (baseModel) in the catalogue", model)
	}
	files := resolveComfyFiles(conn.Files[model])

	w, h, ok := parseSize(req.Size)
	if !ok {
		w, h = 1024, 1024
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	seed, err := comfyRandomSeed()
	if err != nil {
		return Result{}, err
	}
	graph, err := comfyBuildGraph(family, files, comfyParams{Prompt: req.Prompt, Seed: seed, Width: w, Height: h, BatchSize: count})
	if err != nil {
		return Result{}, err
	}

	switchWarning := comfySwitchWarning(conn, model)

	ctx, cancel := context.WithTimeout(ctx, sdcppTimeout)
	defer cancel()

	promptID, err := p.submit(ctx, conn, graph, model)
	if err != nil {
		return Result{}, err
	}
	hist, err := p.awaitHistory(ctx, conn, promptID)
	if err != nil {
		return Result{}, err
	}
	images, err := p.fetchImages(ctx, conn, hist)
	if err != nil {
		return Result{}, err
	}

	warnings := comfyWarnings(req)
	if switchWarning != "" {
		warnings = append(warnings, switchWarning)
	}
	return Result{
		Images:      images,
		Provider:    ProviderComfy,
		Model:       model,
		Destination: "the fleet's own GPU engine（この配備が動かす自前のエンジン）",
		Warnings:    warnings,
		CostUSD:     0,
		Usage:       Usage{Measured: false},
	}, nil
}

// comfyWarnings is what this route knows it cannot honour, mirroring sdcppWarnings.
func comfyWarnings(req Request) []string {
	var out []string
	if b := strings.ToLower(strings.TrimSpace(req.Background)); b == "transparent" {
		out = append(out, "background=transparent requested, opaque produced (this engine's checkpoints have no alpha channel)")
	}
	return out
}

// comfyRandomSeed picks a fresh seed per request. Nothing in Request lets a caller pin one —
// ADR 0069's vocabulary is provider-neutral and has no seed field — and ComfyUI caches a node's
// output by its inputs (measured, bench-image-engine.py), so replaying the same graph twice
// would answer the second call from cache in half a second rather than generating anything.
func comfyRandomSeed() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("could not pick a seed: %w", err)
	}
	n := int64(binary.BigEndian.Uint64(b[:]) & math.MaxInt64)
	return n, nil
}

// submit is POST /prompt, with the same retry-on-503-engine_waking loop as sdcpp.go's send —
// this is the ONE call of the three that can hit a stopped engine, so it is the one that has to
// survive the wake.
func (p *comfyProvider) submit(ctx context.Context, conn EngineConn, graph comfyGraph, model string) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": graph, "client_id": "af-agent"})
	if err != nil {
		return "", err
	}
	lastWaking := ""
	for attempt := 1; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sdcppURL(conn, "/prompt"), bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		// Declares which checkpoint this request used, for the gateway's warm-model tracking
		// (ADR 0072 decision 7) — ComfyUI's /prompt answer carries only a queue id, never a
		// model name, so this header is the only way the CP learns what became warm.
		httpReq.Header.Set("X-AF-Model", model)

		respBody, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return "", sdcppGaveUp(attempt, lastWaking)
			}
			return "", err
		}
		if status < 300 {
			var doc struct {
				PromptID string `json:"prompt_id"`
				Error    any    `json:"error"`
			}
			if json.Unmarshal(respBody, &doc) != nil || doc.PromptID == "" {
				return "", fmt.Errorf("the image engine's /prompt answer had no prompt_id: %s", tail(string(respBody), 400))
			}
			return doc.PromptID, nil
		}
		if !sdcppRetryable(status, respBody) {
			return "", fmt.Errorf("the image engine answered %d %s: %s",
				status, http.StatusText(status), sdcppErrText(respBody))
		}
		lastWaking = sdcppErrText(respBody)
		select {
		case <-ctx.Done():
			return "", sdcppGaveUp(attempt, lastWaking)
		case <-time.After(retryAfter):
		}
	}
}

// comfyPollEvery is how often /history is asked once the engine has accepted the prompt (i.e.
// after submit already succeeded, so the engine is confirmed up and this is not the wake dance).
// A var only so a test can shorten it.
var comfyPollEvery = 1 * time.Second

// comfyHistory is the fields this package reads out of GET /history/<id>. ComfyUI's own document
// nests one more level (keyed by the prompt id itself), which awaitHistory unwraps.
type comfyHistory struct {
	Status struct {
		Completed bool   `json:"completed"`
		StatusStr string `json:"status_str"`
		Messages  []any  `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Images []struct {
			Filename  string `json:"filename"`
			Subfolder string `json:"subfolder"`
			Type      string `json:"type"`
		} `json:"images"`
	} `json:"outputs"`
}

// awaitHistory polls until ComfyUI reports the queued prompt done (success or error), bounded by
// ctx — the same overall budget submit's caller set, so a generation that never finishes is cut
// off by the request's own timeout rather than looping forever.
func (p *comfyProvider) awaitHistory(ctx context.Context, conn EngineConn, promptID string) (comfyHistory, error) {
	for {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sdcppURL(conn, "/history/"+url.PathEscape(promptID)), nil)
		if err != nil {
			return comfyHistory{}, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		body, status, _, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return comfyHistory{}, fmt.Errorf("waiting for the image engine timed out: %w", ctx.Err())
			}
			return comfyHistory{}, err
		}
		if status >= 300 {
			return comfyHistory{}, fmt.Errorf("the image engine's /history answered %d %s: %s",
				status, http.StatusText(status), sdcppErrText(body))
		}
		var byID map[string]comfyHistory
		if err := json.Unmarshal(body, &byID); err != nil {
			return comfyHistory{}, fmt.Errorf("the image engine's /history answer was not JSON: %w", err)
		}
		if hist, ok := byID[promptID]; ok {
			if hist.Status.StatusStr == "error" {
				return comfyHistory{}, fmt.Errorf("the image engine failed the request: %s", comfyErrorMessages(hist))
			}
			if hist.Status.Completed {
				return hist, nil
			}
		}
		select {
		case <-ctx.Done():
			return comfyHistory{}, fmt.Errorf("waiting for the image engine timed out: %w", ctx.Err())
		case <-time.After(comfyPollEvery):
		}
	}
}

// comfyErrorMessages renders ComfyUI's execution_error message list, capped: it carries a full
// Python traceback per node, and the caller only needs enough to know which node and why.
func comfyErrorMessages(hist comfyHistory) string {
	b, err := json.Marshal(hist.Status.Messages)
	if err != nil {
		return "unknown error"
	}
	return tail(string(b), 800)
}

// fetchImages downloads every output image GET /view names, in the order ComfyUI's own outputs
// map iterates — a batch of N (Request.Count) all rides on the SAME node, so this is not
// re-deriving what "count" meant, only reading off what the graph actually produced.
func (p *comfyProvider) fetchImages(ctx context.Context, conn EngineConn, hist comfyHistory) ([]Image, error) {
	var out []Image
	for _, o := range hist.Outputs {
		for _, im := range o.Images {
			img, err := p.viewOne(ctx, conn, im.Filename, im.Subfolder, im.Type)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the image engine reported success but produced no image")
	}
	return out, nil
}

func (p *comfyProvider) viewOne(ctx context.Context, conn EngineConn, filename, subfolder, kind string) (Image, error) {
	q := url.Values{"filename": {filename}, "subfolder": {subfolder}, "type": {kind}}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sdcppURL(conn, "/view?"+q.Encode()), nil)
	if err != nil {
		return Image{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
	body, status, _, err := engineHTTPAttempt(p.client, httpReq)
	if err != nil {
		return Image{}, err
	}
	if status >= 300 {
		return Image{}, fmt.Errorf("the image engine's /view answered %d %s: %s",
			status, http.StatusText(status), sdcppErrText(body))
	}
	img := Image{Bytes: body, MIME: comfyMIMEFor(filename)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	return img, nil
}

// comfyMIMEFor reads the extension because /view answers with the file's own bytes and no JSON
// envelope to carry a declared format in (unlike sdcpp's output_format field) — SaveImage's own
// default, and every template here, produces PNG, so anything else is a future template's doing.
func comfyMIMEFor(filename string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".jpg"), strings.HasSuffix(strings.ToLower(filename), ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(strings.ToLower(filename), ".webp"):
		return "image/webp"
	default:
		return "image/png"
	}
}
