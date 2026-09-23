package imagegen

// openaicompat speaks the OpenAI Images API against any server that implements it: this fleet's
// own GPU (ADR 0071), an operator's LAN box (ADR 0076), a borrowed engine on another fleet (ADR
// 0079), or a metered vendor endpoint — whichever the engine table row behind it names (ADR
// 0083). The transport and the wire shape are the same regardless of which:
//
//   - the transport is the Control Plane's engine gateway, not the engine itself. The Workspace
//     never reaches the far server directly: it POSTs to /engine/image/v1/… on the CP, which
//     relays to the row's URL and, for a row it manages, wakes the box if it is asleep;
//   - the API is OpenAI's own: /v1/images/generations is generate, /v1/images/edits is edit, and
//     the same endpoint with a `mask` is inpaint.
//
// ## Why there IS a retry loop, after all (measured 2026-09-07, ADR 0071 P1)
//
// The first version of this file said the gateway holds the request while a managed box comes
// up, so one HTTP call is all there is. Driven on a real deployment that turned out to be false
// in a way no test could show: this route is not streaming (the server answers JSON), so the
// request sends no byte for as long as the far end takes, and the ingress ALB closes an idle
// connection at 60 seconds. The image engine that prompted this needed about 165. Measured, the
// CP answered `503 59.998s` and the image generation fell through to another provider — spending
// a MEMBER's plan quota on a call the fleet's own hardware was two minutes away from serving,
// which is the exact outcome putting the fleet's own providers first in the order exists to
// avoid.
//
// So the gateway folds its non-streaming wait BELOW the ingress's idle timeout and answers
// `503 engine_waking` + `Retry-After`, and this is the side that keeps asking. One tool call
// still returns one picture: the retries are invisible above this function, and the MCP layer's
// progress heartbeat is what keeps the client's own clock alive through them.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type openaiCompatProvider struct {
	// key is this provider's id everywhere outside this file — the images row's own key (ADR
	// 0082 decision 1). Empty in a hand-built test double, which is why ID() falls back to the
	// bare kind name rather than an empty string.
	key    string
	lookup func(ctx context.Context) (EngineConn, bool)
	client *http.Client
}

func newOpenAICompatProviderFor(key string) *openaiCompatProvider {
	return &openaiCompatProvider{key: key, lookup: engineLookupFor(key), client: engineClient}
}

func (p *openaiCompatProvider) ID() string {
	if p.key != "" {
		return p.key
	}
	return ProviderOpenAICompat
}

// Ready is "this deployment has a row for this provider and we hold a token for it" — never
// "the far server is up". A server that is asleep is a NORMAL state for a managed row and
// waking it is the gateway's job; reporting it as not ready would take the tool out of
// tools/list for the exact reason the whole design exists to make invisible.
func (p *openaiCompatProvider) Ready(ctx context.Context) bool {
	_, ok := p.conn(ctx)
	return ok
}

func (p *openaiCompatProvider) conn(ctx context.Context) (EngineConn, bool) {
	if p.lookup == nil {
		return EngineConn{}, false
	}
	c, ok := p.lookup(ctx)
	if !ok || strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Token) == "" {
		return EngineConn{}, false
	}
	return c, true
}

// DefaultModel is the first id the row declared. For a server that holds one checkpoint chosen
// by a startup flag this is the only model it has; for one that holds several, it is simply
// which one an administrator listed first.
func (p *openaiCompatProvider) DefaultModel() string {
	c, ok := p.conn(context.Background())
	if !ok || len(c.Models) == 0 {
		return ""
	}
	return c.Models[0]
}

// Caps is per (provider, model) — ADR 0069 decision 5 — because what a request may ask for is a
// fact about the checkpoint an engine table row names, not about this client.
//
// Backgrounds is empty on purpose: this client has no way to ask an arbitrary OpenAI-compatible
// server whether its checkpoints produce an alpha channel, so it does not advertise a capability
// nothing here can verify.
func (p *openaiCompatProvider) Caps(model string) Caps {
	conn, _ := p.conn(context.Background())
	if strings.TrimSpace(model) == "" {
		model = p.DefaultModel()
	}
	return Caps{
		// inpaint as well as edit: /v1/images/edits takes an optional `mask`, and a mask is
		// the whole difference between the two ops.
		Ops: []Op{OpGenerate, OpEdit, OpInpaint},
		// Declared only (ADR 0083 decision 2): a row that says nothing about a model's sizes
		// offers none, rather than this client guessing a checkpoint family from the id — a
		// guess that is a claim about Stable Diffusion specifically and this client no longer
		// assumes the far end is one.
		Sizes: conn.Sizes[model],
		// Measured against sd-server with one input image and one mask (ADR 0071 P1). The
		// endpoint has an `image[]` field for more, but a capability nobody has run against a
		// real server is a promise, so this says one.
		MaxInputs: 1,
		MaxCount:  4,
		// No seed, and deliberately not a gap. The OpenAI Images API this client speaks defines
		// no seed field. The one channel that could carry one — `<sd_cpp_extra_args>` inside the
		// prompt text — is the same hole ADR 0072 decision 5 says must be REFUSED when a caller's
		// prompt contains it, because it also passes `lora.path`, a server-side file path.
		// Writing into that hole ourselves would build the injection surface the decision closes.
		// The core reports the dropped seed as a warning instead.
	}
}

// openaiCompatRequestModel is what this call names the checkpoint as: the caller's own choice,
// or the row's first declared id when the caller named none. "" when the row declares no models
// at all, which is the switch decision 2 puts in the operator's hands — a row with no model
// declared sends no `model` field, exactly as it always has.
func openaiCompatRequestModel(conn EngineConn, req Request) string {
	model := strings.TrimSpace(req.Model)
	if model == "" && len(conn.Models) > 0 {
		model = conn.Models[0]
	}
	return model
}

func (p *openaiCompatProvider) Generate(ctx context.Context, req Request) (Result, error) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Result{}, errors.New("this deployment has no reachable OpenAI-compatible image server")
	}
	model := openaiCompatRequestModel(conn, req)
	caps := p.Caps(model)
	if !caps.Supports(req.Op) {
		return Result{}, fmt.Errorf("the image server cannot do %s", req.Op)
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

	ctx, cancel := context.WithTimeout(ctx, engineTimeout)
	defer cancel()

	body, err := p.send(ctx, conn, req)
	if err != nil {
		return Result{}, err
	}

	images, err := openaiCompatDecode(body)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Images: images,
		// The row's own key (ADR 0082 decision 1), not the bare kind name: two openai-compat rows
		// on one deployment answer with different ids, which is what tells them apart in the
		// ledger and in generate_image's own result — `provider: comfy-lan` instead of a bare
		// `openai-compat` that could mean any row of that kind.
		Provider: p.ID(),
		Model:    model,
		// Where the prompt went (ADR 0069 decision 11 / ADR 0083 decision 2). It DID leave the
		// container, which is the fact that matters — and unlike the other providers, the id
		// alone does not say to WHOM: the row can point at this fleet's own GPU or at a metered
		// external service, and that ambiguity survives ADR 0082 P0 too (the key names a row an
		// administrator chose, not a place). This is a FIXED sentence, the same for every row, not
		// a per-row answer.
		Destination: "an OpenAI-compatible image server（宛先はエンジン表の行次第。フリート自身の GPU のこともあれば外部サービスのこともある）",
		Warnings:    openaiCompatWarnings(req),
		// No cost: an engine hour is a component cost the deployment pays and ADR 0048 does
		// not apportion it, so any per-image figure here would be invented.
		CostUSD: 0,
		// No tokens exist on this route at all — there is no driver model. That is "none", not
		// a failure to measure, and the images and pixels the core records ARE exact.
		Usage: Usage{Measured: false},
	}, nil
}

// send performs the request, retrying for as long as the gateway says the engine is on its way
// up and the context allows. Returns the successful body.
//
// The request is rebuilt on every attempt rather than replayed: an edit's body is a multipart
// document that has already been read, and a retried POST that sends an empty body is a bug
// that only appears on the one endpoint that is not JSON.
func (p *openaiCompatProvider) send(ctx context.Context, conn EngineConn, req Request) ([]byte, error) {
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
			httpReq, err = openaiCompatGenerationRequest(ctx, conn, req)
		} else {
			httpReq, err = openaiCompatEditRequest(ctx, conn, req)
		}
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		// Declares which checkpoint this request used, for the gateway's warm-model tracking
		// (ADR 0072 decision 7) — the server's own answer carries no such field (pixels, not a
		// model name), so without this header the admin panel's warm_model never fires for the
		// image role at all, no matter which image provider is running.
		if m := openaiCompatRequestModel(conn, req); m != "" {
			httpReq.Header.Set("X-AF-Model", m)
		}

		body, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			// A transport error with the budget already gone is the same event as the one below
			// — the wait ended — and it must not be reported as a bare "context deadline
			// exceeded". Which of the two paths notices first is a race, so both say the same
			// thing.
			if ctx.Err() != nil {
				return nil, engineGaveUp(attempt, lastWaking)
			}
			return nil, err
		}
		if status < 300 {
			return body, nil
		}
		if !engineRetryable(status, body) {
			// The gateway's own refusals arrive here too — a 503 with "the fleet's own inference
			// engine did not come up in time" is the message a person and the model both need, so
			// it is passed through rather than replaced with the status code.
			return nil, fmt.Errorf("the image engine answered %d %s: %s",
				status, http.StatusText(status), engineErrText(body))
		}
		lastWaking = engineErrText(body)
		select {
		case <-ctx.Done():
			return nil, engineGaveUp(attempt, lastWaking)
		case <-time.After(retryAfter):
		}
	}
}

// sdcppErrCode pulls the machine-readable code out of the gateway's error object. "" for
// anything else, including the far server's own errors, which have no code of this shape.
//
// Kept under its old name: engine.go's engineRetryable calls it by this exact identifier and is
// shared, provider-neutral plumbing this ADR does not touch (ADR 0083 decision 4).
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

// sdcppMaxResponse bounds the answer. Four 1024x1024 PNGs base64-encoded is about 8 MB; 64 MB
// leaves room for a batch of larger ones without letting a misbehaving server grow the Agent's
// memory without limit. Kept under its old name for the same reason as sdcppErrCode.
const sdcppMaxResponse = 64 << 20

// sdcppRetryMin and sdcppRetryMax bound what a Retry-After is allowed to ask for. The floor
// stops a misconfigured gateway turning this into a spin; the ceiling stops one turning a
// 165-second wake into a five-minute one because nobody read the header back. Vars only so a
// test can measure the mechanism instead of the wall clock. Kept under their old names for the
// same reason as sdcppErrCode.
var (
	sdcppRetryMin = 3 * time.Second
	sdcppRetryMax = 30 * time.Second
)

// sdcppRetryAfter reads the header, in the delay-seconds form the gateway sends, and clamps it.
// An absent or unreadable value is not an error: the floor is a perfectly good answer to "come
// back later" and the alternative is giving up on a wake that is already under way. Kept under
// its old name for the same reason as sdcppErrCode.
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

// openaiCompatGenerationRequest builds POST /v1/images/generations.
//
// `model` rides only when a model was resolved (the caller named one, or the row declares a
// default) — a row that names none gets no `model` field, exactly as before this client spoke
// to more than a single-checkpoint server (ADR 0083 decision 2). `response_format` is always
// requested as `b64_json`: some OpenAI-compatible servers default to `url` for some models, and
// this client refuses rather than fetches one (openaiCompatDecode).
func openaiCompatGenerationRequest(ctx context.Context, conn EngineConn, req Request) (*http.Request, error) {
	body := map[string]any{"prompt": req.Prompt, "response_format": "b64_json"}
	if m := openaiCompatRequestModel(conn, req); m != "" {
		body["model"] = m
	}
	if req.Count > 1 {
		body["n"] = req.Count
	}
	if s, ok := openaiCompatSize(req.Size); ok {
		body["size"] = s
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL(conn, "/images/generations"), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// openaiCompatEditRequest builds POST /v1/images/edits, which is multipart/form-data — the one
// endpoint in this system that is not JSON, and the reason the gateway forwards the caller's
// Content-Type instead of stamping application/json on everything (the boundary lives in that
// header). `model` and `response_format` follow the same rules as the generation request.
func openaiCompatEditRequest(ctx context.Context, conn EngineConn, req Request) (*http.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("prompt", req.Prompt); err != nil {
		return nil, err
	}
	if m := openaiCompatRequestModel(conn, req); m != "" {
		if err := mw.WriteField("model", m); err != nil {
			return nil, err
		}
	}
	if err := mw.WriteField("response_format", "b64_json"); err != nil {
		return nil, err
	}
	if req.Count > 1 {
		if err := mw.WriteField("n", strconv.Itoa(req.Count)); err != nil {
			return nil, err
		}
	}
	if s, ok := openaiCompatSize(req.Size); ok {
		if err := mw.WriteField("size", s); err != nil {
			return nil, err
		}
	}
	// `image`, the single-file field, because Caps says one input. The plural `image[]` is
	// what a multi-image edit would use.
	for _, in := range req.Inputs {
		if err := openaiCompatAttach(mw, "image", in); err != nil {
			return nil, err
		}
	}
	if req.Mask != "" {
		if err := openaiCompatAttach(mw, "mask", req.Mask); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL(conn, "/images/edits"), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r, nil
}

// openaiCompatAttach puts one file on the form. The path comes from the caller (a session named
// a file it wants edited), so the failure to read it is reported with the path in it — "no such
// file" with no name is the least actionable answer there is.
func openaiCompatAttach(mw *multipart.Writer, field, path string) error {
	b, err := readRequestFile(path)
	if err != nil {
		return err
	}
	w, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// openaiCompatSize passes a size through when it is a real WIDTHxHEIGHT. "auto" and "" are not
// sizes and are omitted, which lets the server use the checkpoint's own default.
func openaiCompatSize(s string) (string, bool) {
	w, h, ok := parseSize(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%dx%d", w, h), true
}

// openaiCompatWarnings is what this route knows it cannot honour, in the caller's own terms. The
// core adds the rest by comparing the request with what actually arrived (requestWarnings).
func openaiCompatWarnings(req Request) []string {
	var out []string
	if b := strings.ToLower(strings.TrimSpace(req.Background)); b == "transparent" {
		out = append(out, "background=transparent requested, opaque produced (this route cannot promise an alpha channel)")
	}
	return out
}

// openaiCompatDecode reads the OpenAI-compatible answer: data[].b64_json, plus an output_format
// that names the encoding. The dimensions are taken from the decoded bytes rather than from what
// was asked for — the whole point of the warning path is that those two can differ.
//
// A data item carrying `url` instead of `b64_json` is REFUSED, not fetched (ADR 0083 decision
// 2): `response_format: b64_json` is requested on every call, so a url-only answer means the
// server behind this row does not honour it, and following the link would be egress this
// provider has no business making on the caller's behalf.
func openaiCompatDecode(body []byte) ([]Image, error) {
	var doc struct {
		OutputFormat string `json:"output_format"`
		Data         []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the image server's answer was not JSON: %w", err)
	}
	if len(doc.Data) == 0 {
		return nil, errors.New("the image server returned no image")
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
		if strings.TrimSpace(d.B64) == "" {
			if strings.TrimSpace(d.URL) != "" {
				return nil, fmt.Errorf(
					"image %d answered with a url instead of b64_json even though response_format=b64_json was requested; this provider does not fetch one",
					i+1)
			}
			return nil, fmt.Errorf("image %d carried neither b64_json nor url", i+1)
		}
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
