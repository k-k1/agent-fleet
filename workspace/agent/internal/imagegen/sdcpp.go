package imagegen

// sdcpp — the fleet's own image engine (stable-diffusion.cpp's sd-server) as an imagegen
// provider (ADR 0071 P1, the fourth layer of ADR 0069 decision 3).
//
// Two things make this the smallest provider in the package rather than the largest, and
// both were the reason ADR 0071 decision 6 picked sd-server over ComfyUI for P1:
//
//   - the transport is the Control Plane's engine gateway, not the engine. The Workspace
//     never reaches a GPU box: it POSTs to /engine/image/v1/… on the CP, which wakes the box
//     if it is asleep and holds the request while it comes up (decision 4). So there is no
//     start, no health poll and no retry loop here — one HTTP call that may take minutes;
//   - sd-server's OpenAI-compatible face maps one-to-one onto Op: /v1/images/generations is
//     generate, /v1/images/edits is edit, and the same endpoint with a `mask` is inpaint.
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
	// Models are the ids the STACK declared for this engine. The engine is asleep when this
	// is read, so nothing may be asked of it (ADR 0053 / ADR 0071 decision 8).
	Models []string
}

// EngineLookup is the seam the Agent fills in. nil — the normal case — means this deployment
// runs no self-hosted engines, and the provider is simply never ready.
var EngineLookup func(ctx context.Context) (EngineConn, bool)

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
	return &sdcppProvider{lookup: EngineLookup, client: sdcppClient}
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
	if strings.TrimSpace(model) == "" {
		model = p.DefaultModel()
	}
	return Caps{
		// inpaint as well as edit: /v1/images/edits takes an optional `mask`, and a mask is
		// the whole difference between the two ops.
		Ops:   []Op{OpGenerate, OpEdit, OpInpaint},
		Sizes: sdcppSizes(model),
		// Measured with one input image and one mask. The endpoint has an `image[]` field for
		// more, but a capability nobody has run is a promise, so this says one.
		MaxInputs: 1,
		MaxCount:  4,
	}
}

// sdcppSizes is the size list for a checkpoint family, keyed off the declared model id.
//
// Derived from the id rather than declared per model because the alternative is a stack
// parameter listing pixel dimensions per checkpoint, and the ids the fleet stages are its own
// naming. The engine ACCEPTS other sizes — `size` is a plain WIDTHxHEIGHT field — so this is
// the set the tool offers, not a limit it enforces: an unlisted size is passed through and
// whatever comes back is reported by the core as a warning if it differs.
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

func (p *sdcppProvider) Generate(ctx context.Context, req Request) (Result, error) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Result{}, errors.New("this deployment runs no self-hosted image engine")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" && len(conn.Models) > 0 {
		model = conn.Models[0]
	}
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
		return Result{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+conn.Token)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("reaching the image engine failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, sdcppMaxResponse))
	if err != nil {
		return Result{}, fmt.Errorf("reading the image engine's answer failed: %w", err)
	}
	if resp.StatusCode >= 300 {
		// The gateway's own refusals arrive here too — a 503 with "the fleet's own inference
		// engine did not come up in time" is the message a person and the model both need, so
		// it is passed through rather than replaced with the status code.
		return Result{}, fmt.Errorf("the image engine answered %s: %s", resp.Status, sdcppErrText(body))
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
