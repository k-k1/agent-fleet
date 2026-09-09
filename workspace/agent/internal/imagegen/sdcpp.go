package imagegen

// sdcpp — the fleet's own image engine (stable-diffusion.cpp's sd-server) as an imagegen
// provider (ADR 0071 P1, the fourth layer of ADR 0069 decision 3).
//
// Two things make this the smallest provider in the package rather than the largest, and
// both were the reason ADR 0071 decision 6 picked sd-server over ComfyUI for P1:
//
//   - the transport is the Control Plane's engine gateway, not the engine. The Workspace
//     never reaches a GPU box: it POSTs to /engine/image/v1/… on the CP, which wakes the box
//     if it is asleep (decision 4). So there is no start and no health poll here;
//   - sd-server's OpenAI-compatible face maps one-to-one onto Op: /v1/images/generations is
//     generate, /v1/images/edits is edit, and the same endpoint with a `mask` is inpaint.
//
// ## Why there IS a retry loop, after all (measured 2026-09-07, ADR 0071 P1)
//
// The first version of this file said the gateway holds the request while the box comes up, so
// one HTTP call is all there is. Driven on a real deployment that turned out to be false in a
// way no test could show: this route is not streaming (sd-server answers JSON), so the request
// sends no byte for as long as the engine takes, and the ingress ALB closes an idle connection
// at 60 seconds. The image engine needs about 165. Measured, the CP answered
// `503 59.998s` and the image generation fell through to another provider — spending a
// MEMBER's plan quota on a call the fleet's own hardware was two minutes away from serving,
// which is the exact outcome putting sdcpp first in the order exists to avoid.
//
// So the gateway now folds its non-streaming wait BELOW the ingress's idle timeout and answers
// `503 engine_waking` + `Retry-After`, and this is the side that keeps asking. One tool call
// still returns one picture: the retries are invisible above this function, and the MCP layer's
// progress heartbeat is what keeps the client's own clock alive through them.
//
// ⚠️ NOT the native async job API (/sdcpp/v1/img_gen). It is unusable inside a container —
// measured on the GPU box, it answers `filesystem error: /proc/1/map_files … Operation not
// permitted` — while the OpenAI-compatible face works. That is also why the call is
// synchronous: there is no job to poll even if we wanted one.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EngineConn is one reachable self-hosted engine: where to send a request and what to
// present. The Agent's engines.go builds it from the Control Plane's catalogue; this package
// never learns that a Control Plane exists.
type EngineConn struct {
	// BaseURL is absolute and already ends at the engine's /v1, e.g.
	// https://<cp>/engine/image/v1.
	BaseURL string
	Token   string
	// Models are the ids the CATALOGUE declares for this engine. The engine is asleep when
	// this is read, so nothing may be asked of it (ADR 0053 / ADR 0071 decision 8). The FIRST
	// is the one the engine will start with — sd-server holds one checkpoint, chosen by a
	// startup flag — which is why the order matters and is not sorted here.
	Models []string
	// Sizes is the size list DECLARED per model id (ADR 0072 decision 2). Empty for a model
	// whose catalogue entry says nothing, and for a Control Plane older than the catalogue,
	// in which case sdcppSizes falls back to reading the id.
	Sizes map[string][]string
	// BaseModel is decision 2's family label per model id ("sdxl", "sd35", "flux1",
	// "flux2-klein", "zimage", …). sdcpp never reads this — it holds one checkpoint and never
	// asks which family it belongs to — but comfy uses it to pick which workflow template
	// renders the request (ADR 0072 decision 4, phase P2).
	BaseModel map[string]string
	// Files are the on-disk names decision 2 declares for one model — see EngineFile. sdcpp
	// does not use this (it never chooses a checkpoint at request time); comfy reads it to fill
	// in a workflow template's loader nodes.
	Files map[string][]EngineFile
	// Warm is the model id the Control Plane last saw this engine actually answer with (ADR
	// 0072 decision 7's warm_model), or "" when nothing is known to be warm. comfy uses it to
	// pick the default when the caller names no model — a switch costs 1-2.5 minutes of disk
	// re-read (measured), so answering with whatever is already warm is free and answering
	// with an arbitrary "first enabled" model is not.
	Warm string
	// Descriptions is the catalogue's own per-model line (ADR 0072 decision 2), the sentence an
	// agent reads when CHOOSING a checkpoint — "photoreal, SDXL fine-tune" and the like. Empty
	// for a model the catalogue says nothing about, which is not an error: the id alone is a
	// usable, if less helpful, choice.
	Descriptions map[string]string
}

// EngineFile is one file ADR 0072 decision 2 declares for a model: the on-disk basename (the
// fetch sidecar mirrors S3 keys onto disk verbatim, so this is also what ComfyUI's loader nodes
// see under their configured model directory) and, for a split model, the flag sd.cpp's own
// spelling gives that part (`--vae` / `--clip_l` / `--t5xxl` / `--diffusion-model`). Empty Flag
// means a single-file model (a plain checkpoint). comfy re-reads sd.cpp's vocabulary rather than
// inventing a second one for the same fact — decision 2 already declares "which flag" per file,
// and sd.cpp's flags already say what each part IS regardless of which engine loads them.
type EngineFile struct {
	Flag string
	Name string
}

// EngineLookup is the seam the Agent fills in, keyed by the CALLING provider's id so that
// sdcpp and comfy — mutually exclusive on one deployment (ADR 0072 decision 4) — each get the
// engine row that actually matches them rather than whichever the `image` role happens to be
// running today. nil, or a lookup that finds no row for this id, means this deployment does
// not run that provider, and it is simply never ready.
var EngineLookup func(ctx context.Context, provider string) (EngineConn, bool)

// sdcppTimeout bounds one call, and it is deliberately LONGER than the gateway's own wake
// timeout (AF_ENGINE_WAKE_TIMEOUT, 900 s by default). The chain is
// gateway 900 s < this 960 s < the MCP layer's budget: whoever gives up first decides what
// the model is told, and the gateway is the only one of the three that knows WHY the wait was
// long ("the box did not come up", "the engine answered 500"). A bare client-side timeout
// here would replace that with nothing.
//
// A warm engine is nowhere near this: measured on an L4, 512px in 7.8 s and 1024px in 21 s.
// The budget is for the cold case — task creation to listening was 195 s, on top of however
// long a GPU box takes to appear (8 to 88 s across P0's measurements).
const sdcppTimeout = 16 * time.Minute

// sdcppClient has no timeout of its own: the bound is the context, so that a caller who hung
// up ends the request immediately rather than at the far end of a 16-minute clock.
var sdcppClient = &http.Client{}

type sdcppProvider struct {
	lookup func(ctx context.Context) (EngineConn, bool)
	client *http.Client
}

func newSdcppProvider() *sdcppProvider {
	return &sdcppProvider{lookup: engineLookupFor(ProviderSdcpp), client: sdcppClient}
}

// engineLookupFor binds the package-level, provider-keyed EngineLookup to one id, giving each
// provider struct the single-argument shape its own tests already construct directly. nil when
// EngineLookup itself is nil (the normal case for a deployment with no engines at all), so a
// provider's lookup field is never a closure that panics on a nil call.
func engineLookupFor(provider string) func(ctx context.Context) (EngineConn, bool) {
	if EngineLookup == nil {
		return nil
	}
	return func(ctx context.Context) (EngineConn, bool) { return EngineLookup(ctx, provider) }
}

func (p *sdcppProvider) ID() string { return ProviderSdcpp }

// Ready is "this deployment has an image engine and we hold a token for it" — never "the
// engine is up". An engine that is asleep is the NORMAL state and waking it is the gateway's
// job; reporting it as not ready would take the tool out of tools/list for the exact reason
// the whole design exists to make invisible.
func (p *sdcppProvider) Ready(ctx context.Context) bool {
	_, ok := p.conn(ctx)
	return ok
}

func (p *sdcppProvider) conn(ctx context.Context) (EngineConn, bool) {
	if p.lookup == nil {
		return EngineConn{}, false
	}
	c, ok := p.lookup(ctx)
	if !ok || strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Token) == "" {
		return EngineConn{}, false
	}
	return c, true
}

// sdcppDriverModel names the checkpoint for the status route (driverModelOf), without waking
// anything: the answer comes from what the stack declared, which the Agent already holds.
func sdcppDriverModel() string { return newSdcppProvider().DefaultModel() }

// DefaultModel is the first id the stack declared, which is also the checkpoint sd-server was
// started with — the server holds ONE model, chosen by a startup flag, and has no way to
// switch at request time.
func (p *sdcppProvider) DefaultModel() string {
	c, ok := p.conn(context.Background())
	if !ok || len(c.Models) == 0 {
		return ""
	}
	return c.Models[0]
}

// Caps is per (provider, model), which here means per CHECKPOINT — the point ADR 0069
// decision 5 makes, and this provider is where it stops being theoretical: SDXL and SD 1.5
// are the same code path, the same endpoint and the same flags, and they differ only in what
// sizes produce a picture rather than a smear.
//
// Backgrounds is empty on purpose: no Stable Diffusion checkpoint here produces an alpha
// channel, so "transparent" is a request this route cannot meet, and Generate says so in the
// warnings rather than this list implying otherwise.
func (p *sdcppProvider) Caps(model string) Caps {
	conn, _ := p.conn(context.Background())
	if strings.TrimSpace(model) == "" {
		model = p.DefaultModel()
	}
	return Caps{
		// inpaint as well as edit: /v1/images/edits takes an optional `mask`, and a mask is
		// the whole difference between the two ops.
		Ops:   []Op{OpGenerate, OpEdit, OpInpaint},
		Sizes: sdcppDeclaredSizes(conn, model),
		// Measured with one input image and one mask. The endpoint has an `image[]` field for
		// more, but a capability nobody has run is a promise, so this says one.
		MaxInputs: 1,
		MaxCount:  4,
	}
}

// sdcppDeclaredSizes prefers the catalogue's own declaration and falls back to reading the
// model id (ADR 0072 decision 2, phase P0 item 10).
//
// The fallback is not dead code and is not going away soon: a catalogue row seeded from an
// ADR 0071 stack declares no sizes at all, and a Control Plane older than the catalogue sends
// none either. Guessing from the id is what those deployments have always had, and it is
// better than an empty list — an empty Sizes reads as "this provider offers no sizes".
func sdcppDeclaredSizes(conn EngineConn, model string) []string {
	if s := conn.Sizes[model]; len(s) > 0 {
		return s
	}
	return sdcppSizes(model)
}

// sdcppSizes is the size list for a checkpoint family, guessed from the model id. The engine
// ACCEPTS other sizes — `size` is a plain WIDTHxHEIGHT field — so this is the set the tool
// offers, not a limit it enforces: an unlisted size is passed through and whatever comes back
// is reported by the core as a warning if it differs.
func sdcppSizes(model string) []string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "xl") || strings.Contains(m, "sd3") || strings.Contains(m, "flux"):
		// Trained at 1024²; the measured 1024px generation took 20.8-21.0 s on an L4.
		return []string{"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"}
	case strings.Contains(m, "1-5") || strings.Contains(m, "v15") || strings.Contains(m, "1.5"):
		return []string{"512x512", "512x768", "768x512"}
	default:
		// An id this file does not recognise. Both are sizes every checkpoint in the family
		// handles, and neither is a claim about what the model was trained for.
		return []string{"512x512", "1024x1024"}
	}
}

// sdcppRequestModel is what this call names the checkpoint as: the caller's own choice, or the
// engine's started-with default (sd-server holds one, chosen at startup — there is no other).
func sdcppRequestModel(conn EngineConn, req Request) string {
	model := strings.TrimSpace(req.Model)
	if model == "" && len(conn.Models) > 0 {
		model = conn.Models[0]
	}
	return model
}

func (p *sdcppProvider) Generate(ctx context.Context, req Request) (Result, error) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Result{}, errors.New("this deployment runs no self-hosted image engine")
	}
	model := sdcppRequestModel(conn, req)
	caps := p.Caps(model)
	if !caps.Supports(req.Op) {
		return Result{}, fmt.Errorf("the self-hosted image engine cannot do %s", req.Op)
	}
	if req.Prompt == "" {
		return Result{}, errors.New("a prompt is required")
	}
	if len(req.Inputs) > caps.MaxInputs {
		return Result{}, fmt.Errorf("at most %d reference image (got %d)", caps.MaxInputs, len(req.Inputs))
	}
	if req.Op == OpInpaint && req.Mask == "" {
		return Result{}, errors.New("inpaint needs a mask image")
	}
	if req.Op != OpGenerate && len(req.Inputs) == 0 {
		return Result{}, fmt.Errorf("%s needs an input image", req.Op)
	}

	ctx, cancel := context.WithTimeout(ctx, sdcppTimeout)
	defer cancel()

	body, err := p.send(ctx, conn, req)
	if err != nil {
		return Result{}, err
	}

	images, err := sdcppDecode(body)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Images:   images,
		Provider: ProviderSdcpp,
		Model:    model,
		// Where the prompt went (ADR 0069 decision 11 / ADR 0071 decision 10). It DID leave
		// the container, which is the fact that matters — but not to a vendor, and a caller
		// told only "provider: sdcpp" has no way to know which of the two it was.
		Destination: "the fleet's own GPU engine（この配備が動かす自前のエンジン）",
		Warnings:    sdcppWarnings(req),
		// No cost: an engine hour is a component cost the deployment pays and ADR 0048 does
		// not apportion it, so any per-image figure here would be invented.
		CostUSD: 0,
		// No tokens exist on this route at all — there is no driver model. That is "none", not
		// a failure to measure, and the images and pixels the core records ARE exact.
		Usage: Usage{Measured: false},
	}, nil
}

// sdcppMaxResponse bounds the answer. Four 1024x1024 PNGs base64-encoded is about 8 MB; 64 MB
// leaves room for a batch of larger ones without letting a misbehaving engine grow the
// Agent's memory without limit.
const sdcppMaxResponse = 64 << 20

// sdcppRetryMin and sdcppRetryMax bound what a Retry-After is allowed to ask for. The floor
// stops a misconfigured gateway turning this into a spin; the ceiling stops one turning a
// 165-second wake into a five-minute one because nobody read the header back. Vars only so a
// test can measure the mechanism instead of the wall clock.
var (
	sdcppRetryMin = 3 * time.Second
	sdcppRetryMax = 30 * time.Second
)

// send performs the request, retrying for as long as the gateway says the engine is on its way
// up and the context allows. Returns the successful body.
//
// The request is rebuilt on every attempt rather than replayed: an edit's body is a multipart
// document that has already been read, and a retried POST that sends an empty body is a bug
// that only appears on the one endpoint that is not JSON.
func (p *sdcppProvider) send(ctx context.Context, conn EngineConn, req Request) ([]byte, error) {
	// lastWaking is what the gateway last said while the engine was on its way up. Kept so the
	// give-up message can name it: the budget can run out INSIDE a request as easily as between
	// two, and a caller who waited a quarter of an hour deserves the same answer either way.
	lastWaking := ""
	for attempt := 1; ; attempt++ {
		var (
			httpReq *http.Request
			err     error
		)
		if req.Op == OpGenerate {
			httpReq, err = sdcppGenerationRequest(ctx, conn, req)
		} else {
			httpReq, err = sdcppEditRequest(ctx, conn, req)
		}
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		// Declares which checkpoint this request used, for the gateway's warm-model tracking
		// (ADR 0072 decision 7) — sd-server's own answer carries no such field (pixels, not a
		// model name), so without this header the admin panel's warm_model never fires for the
		// image role at all, sdcpp or comfy alike.
		if m := sdcppRequestModel(conn, req); m != "" {
			httpReq.Header.Set("X-AF-Model", m)
		}

		body, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			// A transport error with the budget already gone is the same event as the one below
			// — the wait ended — and it must not be reported as a bare "context deadline
			// exceeded". Which of the two paths notices first is a race, so both say the same
			// thing.
			if ctx.Err() != nil {
				return nil, sdcppGaveUp(attempt, lastWaking)
			}
			return nil, err
		}
		if status < 300 {
			return body, nil
		}
		if !sdcppRetryable(status, body) {
			// The gateway's own refusals arrive here too — a 503 with "the fleet's own inference
			// engine did not come up in time" is the message a person and the model both need, so
			// it is passed through rather than replaced with the status code.
			return nil, fmt.Errorf("the image engine answered %d %s: %s",
				status, http.StatusText(status), sdcppErrText(body))
		}
		lastWaking = sdcppErrText(body)
		select {
		case <-ctx.Done():
			return nil, sdcppGaveUp(attempt, lastWaking)
		case <-time.After(retryAfter):
		}
	}
}

// sdcppGaveUp is the end of the budget, said in terms of what was actually happening. A bare
// "context deadline exceeded" after a quarter of an hour of waking a GPU box tells nobody what
// to do next, and this message is read by a person AND by the model that asked for the picture.
func sdcppGaveUp(attempts int, lastWaking string) error {
	if lastWaking == "" {
		return fmt.Errorf("the fleet's own image engine did not come up within %s (%d attempts)",
			sdcppTimeout, attempts)
	}
	return fmt.Errorf("the fleet's own image engine did not come up within %s (%d attempts): %s",
		sdcppTimeout, attempts, lastWaking)
}

// attempt is one round trip, with the body fully read so the connection can be reused for the
// next one. status is 0 only when err is set.
// engineHTTPAttempt is shared with comfy.go — the same gateway, the same retry contract
// (503 engine_waking, 502/504 from an ingress), so one round-trip helper is enough for both.
func engineHTTPAttempt(client *http.Client, httpReq *http.Request) (body []byte, status int, retryAfter time.Duration, err error) {
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reaching the image engine failed: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, sdcppMaxResponse))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading the image engine's answer failed: %w", err)
	}
	return body, resp.StatusCode, sdcppRetryAfter(resp.Header.Get("Retry-After")), nil
}

// sdcppRetryable decides whether asking again can plausibly do better.
//
//   - the gateway says 503 for three different facts, so the CODE decides and not the number:
//     `engine_waking` is "on its way, ask again"; `engine_off` is an admin switch and
//     `engine_unavailable` is a start that failed, and asking either of those again for fifteen
//     minutes would turn a clear refusal into a hang.
//   - 504 and 502 are an INGRESS, not the gateway: a proxy between the two decided the wait was
//     too long and answered on its behalf. That is the failure this loop was written for. It
//     stays retryable even though the CP now folds its wait below the ALB's, because the next
//     deployment's proxy is not this one's — and it is also what a Control Plane too old to
//     know `engine_waking` looks like from here, since its 900-second hold never survives to
//     answer at all.
func sdcppRetryable(status int, body []byte) bool {
	switch status {
	case http.StatusGatewayTimeout, http.StatusBadGateway:
		return true
	case http.StatusServiceUnavailable:
		return sdcppErrCode(body) == "engine_waking"
	}
	return false
}

// sdcppErrCode pulls the machine-readable code out of the gateway's error object. "" for
// anything else, including the engine's own errors, which have no code of this shape.
func sdcppErrCode(body []byte) string {
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	return doc.Error.Code
}

// sdcppRetryAfter reads the header, in the delay-seconds form the gateway sends, and clamps it.
// An absent or unreadable value is not an error: the floor is a perfectly good answer to "come
// back later" and the alternative is giving up on a wake that is already under way.
func sdcppRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return sdcppRetryMin
	}
	d := time.Duration(n) * time.Second
	if d < sdcppRetryMin {
		return sdcppRetryMin
	}
	if d > sdcppRetryMax {
		return sdcppRetryMax
	}
	return d
}

// sdcppGenerationRequest builds POST /v1/images/generations. Only the fields sd-server
// documents are sent: prompt, n and size (WIDTHxHEIGHT). `model` is deliberately absent —
// the server holds one checkpoint, chosen at startup, and sending a model id it does not use
// would suggest it could be switched.
func sdcppGenerationRequest(ctx context.Context, conn EngineConn, req Request) (*http.Request, error) {
	body := map[string]any{"prompt": req.Prompt}
	if req.Count > 1 {
		body["n"] = req.Count
	}
	if s, ok := sdcppSize(req.Size); ok {
		body["size"] = s
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, sdcppURL(conn, "/images/generations"), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// sdcppEditRequest builds POST /v1/images/edits, which is multipart/form-data — the one
// endpoint in this system that is not JSON, and the reason the gateway forwards the caller's
// Content-Type instead of stamping application/json on everything (the boundary lives in that
// header).
func sdcppEditRequest(ctx context.Context, conn EngineConn, req Request) (*http.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("prompt", req.Prompt); err != nil {
		return nil, err
	}
	if req.Count > 1 {
		if err := mw.WriteField("n", strconv.Itoa(req.Count)); err != nil {
			return nil, err
		}
	}
	if s, ok := sdcppSize(req.Size); ok {
		if err := mw.WriteField("size", s); err != nil {
			return nil, err
		}
	}
	// `image`, the single-file field, because Caps says one input. The plural `image[]` is
	// what a multi-image edit would use.
	for _, in := range req.Inputs {
		if err := sdcppAttach(mw, "image", in); err != nil {
			return nil, err
		}
	}
	if req.Mask != "" {
		if err := sdcppAttach(mw, "mask", req.Mask); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, sdcppURL(conn, "/images/edits"), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r, nil
}

// sdcppAttach puts one file on the form. The path comes from the caller (a session named a
// file it wants edited), so the failure to read it is reported with the path in it — "no such
// file" with no name is the least actionable answer there is.
func sdcppAttach(mw *multipart.Writer, field, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	w, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func sdcppURL(conn EngineConn, path string) string {
	return strings.TrimRight(conn.BaseURL, "/") + path
}

// sdcppSize passes a size through when it is a real WIDTHxHEIGHT. "auto" and "" are not sizes
// and are omitted, which lets the engine use the checkpoint's own default.
func sdcppSize(s string) (string, bool) {
	w, h, ok := parseSize(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%dx%d", w, h), true
}

// sdcppWarnings is what this route knows it cannot honour, in the caller's own terms. The
// core adds the rest by comparing the request with what actually arrived (requestWarnings).
func sdcppWarnings(req Request) []string {
	var out []string
	if b := strings.ToLower(strings.TrimSpace(req.Background)); b == "transparent" {
		out = append(out, "background=transparent requested, opaque produced (this engine's checkpoints have no alpha channel)")
	}
	return out
}

// sdcppDecode reads the OpenAI-compatible answer: data[].b64_json, plus an output_format that
// names the encoding. The dimensions are taken from the decoded bytes rather than from what
// was asked for — the whole point of the warning path is that those two can differ.
func sdcppDecode(body []byte) ([]Image, error) {
	var doc struct {
		OutputFormat string `json:"output_format"`
		Data         []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the image engine's answer was not JSON: %w", err)
	}
	if len(doc.Data) == 0 {
		return nil, errors.New("the image engine returned no image")
	}
	mime := "image/png"
	switch strings.ToLower(strings.TrimSpace(doc.OutputFormat)) {
	case "jpeg", "jpg":
		mime = "image/jpeg"
	case "webp":
		mime = "image/webp"
	}
	out := make([]Image, 0, len(doc.Data))
	for i, d := range doc.Data {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(d.B64))
		if err != nil {
			return nil, fmt.Errorf("image %d was not base64: %w", i+1, err)
		}
		img := Image{Bytes: raw, MIME: mime}
		// A format DecodeConfig does not know (webp is not registered) leaves the dimensions
		// at zero rather than guessing — the same rule the Codex route follows.
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
			img.Width, img.Height = cfg.Width, cfg.Height
		}
		out = append(out, img)
	}
	return out, nil
}

// sdcppErrText pulls the message out of an error body, whichever of the two shapes it is in —
// the gateway's {"error":{"message":…}} or sd-server's own — and falls back to the raw text.
func sdcppErrText(body []byte) string {
	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &doc) == nil {
		if doc.Error.Message != "" {
			return doc.Error.Message
		}
		if doc.Message != "" {
			return doc.Message
		}
	}
	return tail(strings.TrimSpace(string(body)), 400)
}
