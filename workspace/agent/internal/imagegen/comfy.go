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
// than reinvented. /prompt is where a COLD engine is met, but it is not the only call that can
// meet a waking one: the box can be replaced between the submit and the poll that follows, and
// a caller was measured receiving `/history answered 503 … retry` from a poll that then retried
// nothing (ADR 0072 欠落 9). So awaitHistory waits a retryable answer out too.
//
// Every checkpoint switch — including the very first request against a just-started engine — is
// EBS-read time on top of generation (measured 1-2.5 minutes, ADR 0072 "実測で解けた点" 5), which
// is why this file is careful to warn about it (comfySwitchWarning) rather than let a caller read
// a slow answer as a broken one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// comfyDriverModel names the checkpoint for the status route (driverModelOf), without waking
// anything: DefaultModel reads the connection the Control Plane already handed us — the warm
// model, else the catalogue's first — and asks the engine nothing.
func comfyDriverModel() string { return newComfyProvider().DefaultModel() }

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
// Ops is all three. The per-family image-to-image graph P2 left out is comfyRequestLatent, and
// like the LoRA chains it is pinned by shape and not yet proven on a GPU.
func (p *comfyProvider) Caps(model string) Caps {
	conn, _ := p.conn(context.Background())
	if strings.TrimSpace(model) == "" {
		model = p.DefaultModel()
	}
	return Caps{
		// edit and inpaint since ADR 0072 P2's remaining work: each family now has an
		// image-to-image path (LoadImage + VAEEncode, plus SetLatentNoiseMask for a mask), which
		// is what the P2 note said was missing rather than out of reach.
		Ops:   []Op{OpGenerate, OpEdit, OpInpaint},
		Sizes: comfySizesFor(conn, model),
		// One, like sdcpp, and for the same reason: a second reference image would need a graph
		// that stitches or conditions on both, and no such graph has been run here.
		MaxInputs: 1,
		MaxCount:  4,
		Loras:     comfyLoraInfos(conn),
		// The one route where a seed reaches the sampler: it is this package that builds the
		// graph, so the seed is an input this file writes rather than a field a vendor API has
		// to expose (ADR 0069 follow-up, seed).
		Seed: true,
	}
}

// comfyLoraInfos is every LoRA the catalogue enables for this engine (ADR 0072 decision 5, phase
// P3), NOT the ones that fit `model` — see Caps.Loras for why an enum may not depend on another
// argument. The family goes out with each entry so the caller can pair them itself; a pairing
// that does not fit is refused by comfyResolveLoras when the request arrives.
func comfyLoraInfos(conn EngineConn) []LoraInfo {
	if len(conn.Loras) == 0 {
		return nil
	}
	out := make([]LoraInfo, 0, len(conn.Loras))
	for _, l := range conn.Loras {
		if strings.TrimSpace(l.ID) == "" || strings.TrimSpace(l.File) == "" {
			continue // a row with no id or no file cannot be named or loaded
		}
		out = append(out, LoraInfo{Name: l.ID, Description: l.Description, BaseModel: l.BaseModel})
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	got := comfyFamily(strings.TrimSpace(conn.BaseModel[model]))
	for _, f := range comfyFamilies {
		if f == got {
			return f, true
		}
	}
	return "", false
}

// errComfyFamilyNotDeclared separates the two ways this fails, because the fix differs and only
// one of them looks wrong on the admin screen. NOTHING declared is a row the catalogue seeded
// (the seed cannot know a family) or one written before the Control Plane validated it. SOMETHING
// declared that names no template is almost always an upstream display name — "SDXL 1.0",
// "Flux.1 D" — which is what Hugging Face and Civitai publish and what the ingest path used to
// store; that row looks complete in the panel and fails only here.
func errComfyFamilyNotDeclared(model, declared string) error {
	if d := strings.TrimSpace(declared); d != "" {
		return fmt.Errorf("model %s declares the checkpoint family %q, which names no workflow template"+
			" — the catalogue's base_model has to be one of %s", model, d, comfyFamilyList())
	}
	return fmt.Errorf("model %s declares no checkpoint family, so there is no workflow template to build"+
		" — set the catalogue's base_model to one of %s", model, comfyFamilyList())
}

func errUnknownComfyFamily(family comfyFamily) error {
	return fmt.Errorf("no workflow template for checkpoint family %q", string(family))
}

// comfyMaxLoras bounds one request's chain. Each entry is a node ComfyUI loads a file for, and
// four already stacks more style than anyone can steer; the cap exists so a caller cannot turn
// one call into an unbounded pile of disk reads on a box the deployment pays for by the hour.
const comfyMaxLoras = 4

// comfyMaxLoraWeight is decision 5's declared range, 0-2. LoraLoader itself accepts -100 to 100,
// which is a knob for someone watching the result, not for a model that cannot see the picture.
const comfyMaxLoraWeight = 2.0

// comfyResolveLoras turns the request's LoRA names into the chain a template renders, and is
// where ADR 0072's refusal lives (decision 5, レビュー決定 5): it is the AGENT that says no, not
// the Control Plane, because the pairing ends up inside a workflow graph and the gateway must
// not read request bodies to police one (decision 4's "素通し").
//
// The mismatch it refuses — an SD1.5 LoRA asked for on an SDXL checkpoint — has no failure of its
// own: the tensor names simply do not match, and the engine either warns and ignores them or
// produces a quietly degraded picture. Both reach the caller as "the LoRA did nothing", which is
// indistinguishable from a bug in the prompt. So it is refused before any GPU is woken.
func comfyResolveLoras(conn EngineConn, family comfyFamily, model string, want []LoraRef) ([]comfyLora, error) {
	if len(want) == 0 {
		return nil, nil
	}
	if len(conn.Loras) == 0 {
		return nil, fmt.Errorf("no LoRA is enabled on this engine, so %q cannot be applied"+
			" — enable one in the admin panel's model catalogue first", want[0].Name)
	}
	if len(want) > comfyMaxLoras {
		return nil, fmt.Errorf("%d LoRAs asked for, and this route applies at most %d in one request", len(want), comfyMaxLoras)
	}
	byName := map[string]EngineLora{}
	for _, l := range conn.Loras {
		byName[l.ID] = l
	}
	out := make([]comfyLora, 0, len(want))
	seen := map[string]bool{}
	for _, w := range want {
		l, ok := byName[w.Name]
		if !ok || strings.TrimSpace(l.File) == "" {
			return nil, fmt.Errorf("no LoRA named %q on this engine — the catalogue enables %s",
				w.Name, comfyLoraNameList(conn))
		}
		if seen[w.Name] {
			return nil, fmt.Errorf("LoRA %q asked for twice; name it once with the strength you want", w.Name)
		}
		seen[w.Name] = true
		if got := comfyFamily(strings.TrimSpace(l.BaseModel)); got != family {
			return nil, errComfyLoraFamilyMismatch(w.Name, l.BaseModel, model, family)
		}
		weight := w.Weight
		if weight == 0 {
			weight = 1 // "not stated" — see LoraRef.Weight
		}
		if weight < 0 || weight > comfyMaxLoraWeight {
			return nil, fmt.Errorf("LoRA %q asked for at strength %g, and the range is 0-%g",
				w.Name, w.Weight, comfyMaxLoraWeight)
		}
		out = append(out, comfyLora{Name: l.File, Weight: weight})
	}
	return out, nil
}

// errComfyLoraFamilyMismatch separates the two ways a pairing fails, because an operator fixes
// them differently: a LoRA that declares a DIFFERENT family was registered for other checkpoints
// and is being used on the wrong one, while a LoRA that declares NOTHING is a catalogue row
// nobody finished — and that row would otherwise be paired with anything at all.
func errComfyLoraFamilyMismatch(name, declared, model string, family comfyFamily) error {
	if d := strings.TrimSpace(declared); d != "" {
		return fmt.Errorf("LoRA %s was trained for the %s checkpoint family and %s is %s"+
			" — they cannot be combined; a mismatched LoRA does not fail, it quietly does nothing to the picture",
			name, d, model, string(family))
	}
	return fmt.Errorf("LoRA %s declares no checkpoint family, so there is no way to tell whether it fits %s (%s)"+
		" — set the catalogue's base_model to one of %s", name, model, string(family), comfyFamilyList())
}

// comfyLoraNameList spells the enabled LoRAs with the family each belongs to, because "that name
// does not exist" without the alternatives costs the caller another turn to find out what does.
func comfyLoraNameList(conn EngineConn) string {
	out := make([]string, 0, len(conn.Loras))
	for _, l := range conn.Loras {
		if l.BaseModel != "" {
			out = append(out, l.ID+" ("+l.BaseModel+")")
			continue
		}
		out = append(out, l.ID)
	}
	return strings.Join(out, ", ")
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
		return Result{}, errComfyFamilyNotDeclared(model, conn.BaseModel[model])
	}
	if err := comfyCheckInputs(req, caps); err != nil {
		return Result{}, err
	}
	files := resolveComfyFiles(conn.Files[model])
	loras, err := comfyResolveLoras(conn, family, model, req.Loras)
	if err != nil {
		return Result{}, err
	}

	w, h, ok := parseSize(req.Size)
	if !ok {
		w, h = 1024, 1024
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	seed, err := comfySeedFor(req)
	if err != nil {
		return Result{}, err
	}
	params := comfyParams{
		Op: req.Op, Prompt: req.Prompt, Seed: seed, Width: w, Height: h,
		BatchSize: count, Loras: loras,
	}

	switchWarning := comfySwitchWarning(conn, model)

	ctx, cancel := context.WithTimeout(ctx, sdcppTimeout)
	defer cancel()

	// The uploads come FIRST, and not only because the graph has to name them: they are now the
	// call that meets a cold engine, so they carry the wake retry /prompt used to be alone in
	// needing. Reading the picture's real dimensions here rather than trusting req.Size is what
	// keeps klein's schedule honest — Flux2Scheduler derives its shift from a width and height,
	// and an edit's size is the input picture's, not the caller's.
	var sizeWarning string
	if params.isImageToImage() {
		up, err := p.uploadImage(ctx, conn, req.Inputs[0])
		if err != nil {
			return Result{}, err
		}
		params.Image = up.name
		if up.width > 0 && up.height > 0 {
			if req.Size != "" && req.Size != "auto" && (up.width != w || up.height != h) {
				sizeWarning = fmt.Sprintf(
					"size=%s requested, but %s keeps the input picture's own %dx%d", req.Size, req.Op, up.width, up.height)
			}
			params.Width, params.Height = up.width, up.height
		}
		if req.Op == OpInpaint {
			mask, err := p.uploadImage(ctx, conn, req.Mask)
			if err != nil {
				return Result{}, err
			}
			params.Mask = mask.name
		}
	}

	graph, err := comfyBuildGraph(family, files, params)
	if err != nil {
		return Result{}, err
	}

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
	if sizeWarning != "" {
		warnings = append(warnings, sizeWarning)
	}
	if cached := comfyCacheWarning(hist); cached != "" {
		warnings = append(warnings, cached)
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

// comfyCheckInputs refuses a request whose op and attachments do not match, before anything is
// uploaded and before a GPU is woken. The same three rules sdcpp checks, in the same order.
func comfyCheckInputs(req Request, caps Caps) error {
	if len(req.Inputs) > caps.MaxInputs {
		return fmt.Errorf("at most %d reference image (got %d)", caps.MaxInputs, len(req.Inputs))
	}
	if req.Op != OpGenerate && len(req.Inputs) == 0 {
		return fmt.Errorf("%s needs an input image", req.Op)
	}
	if req.Op == OpInpaint && strings.TrimSpace(req.Mask) == "" {
		return errors.New("inpaint needs a mask image")
	}
	return nil
}

// comfyMaxUpload bounds one uploaded picture. The ceiling is not this file's to pick: the engine
// gateway buffers a request body through io.LimitReader at 32 MiB (engineMaxRequestBody), and
// LimitReader TRUNCATES rather than failing — so a larger picture would arrive at ComfyUI as a
// corrupt file and be refused with a decoder error naming nothing the caller can act on. Refusing
// here says which file and how big.
const comfyMaxUpload = 24 << 20

// comfyUpload is what POST /upload/image answered: the name the engine filed the picture under,
// plus the dimensions read locally on the way past.
type comfyUpload struct {
	name          string
	width, height int
}

// uploadImage puts one local file into ComfyUI's own input directory and answers with the name a
// graph may then reference.
//
// This call exists because LoadImage's `image` input is an ENUMERATION over that directory
// (nodes.py, v0.34.0) — there is no "load this path" node, and a path from this container would
// mean nothing on the engine's disk anyway. It is the same shape of trap SD3.5's TripleCLIPLoader
// was: a value that looks like a file name and is really a member of a list the server builds.
//
// The uploaded name is the file's own CONTENT HASH, which buys two things. ComfyUI renames a
// colliding upload to `x (1).png` unless the bytes are identical, so a fixed name would leave a
// growing pile of near-duplicates in the input directory; a hash collides only with itself, and
// the server then recognises the duplicate and keeps the one it has. The extension is preserved
// because the enum LoadImage builds is filtered by content type, which is read off the name.
//
// 🔴 The answer's `name` is used, never the one that was sent. They differ exactly when the server
// decided to rename, and a graph naming the file it MEANT to upload would fail validation against
// a directory listing that has the other one.
func (p *comfyProvider) uploadImage(ctx context.Context, conn EngineConn, path string) (comfyUpload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return comfyUpload{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	if len(raw) > comfyMaxUpload {
		return comfyUpload{}, fmt.Errorf("%s is %d bytes, over this route's %d-byte limit for one picture",
			path, len(raw), comfyMaxUpload)
	}
	up := comfyUpload{name: comfyUploadName(raw, path)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		up.width, up.height = cfg.Width, cfg.Height
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", up.name)
	if err != nil {
		return comfyUpload{}, err
	}
	if _, err := part.Write(raw); err != nil {
		return comfyUpload{}, err
	}
	// type=input is where LoadImage looks by default, so the graph can name the file with no
	// `[type]` annotation. Deliberately no overwrite: identical bytes are recognised as a
	// duplicate and nothing is written at all.
	for k, v := range map[string]string{"type": "input", "subfolder": ""} {
		if err := mw.WriteField(k, v); err != nil {
			return comfyUpload{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return comfyUpload{}, err
	}
	body := buf.Bytes()
	ctype := mw.FormDataContentType()

	answer, err := p.sendWithWake(ctx, conn, "/upload/image", func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sdcppURL(conn, "/upload/image"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", ctype)
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		return httpReq, nil
	})
	if err != nil {
		return comfyUpload{}, err
	}
	var doc struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if json.Unmarshal(answer, &doc) != nil || doc.Name == "" {
		return comfyUpload{}, fmt.Errorf("the image engine's /upload/image answer had no name: %s", tail(string(answer), 400))
	}
	up.name = doc.Name
	if doc.Subfolder != "" {
		// The graph names a path relative to the input directory, the same shape the loras list
		// uses. Nothing here asks for a subfolder, so this only ever fires if a future engine
		// starts choosing one.
		up.name = doc.Subfolder + "/" + doc.Name
	}
	return up, nil
}

// comfyUploadName is the content hash plus an extension ComfyUI's own content-type filter will
// accept. The source file's extension is preferred and the bytes decide when it says nothing —
// a name with no usable extension is one LoadImage's enum drops, which reads as "the upload
// worked and the graph is wrong".
func comfyUploadName(raw []byte, path string) string {
	sum := sha256.Sum256(raw)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
	default:
		switch http.DetectContentType(raw) {
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		default:
			ext = ".png"
		}
	}
	return "af-" + hex.EncodeToString(sum[:8]) + ext
}

// sendWithWake runs one request against the engine through the same retry-on-503-engine_waking
// loop /prompt uses, remaking the request per attempt because a body reader cannot be replayed.
// what names the call in the failure, so a refusal says which of the four endpoints refused.
func (p *comfyProvider) sendWithWake(ctx context.Context, conn EngineConn, what string, make func() (*http.Request, error)) ([]byte, error) {
	lastWaking := ""
	for attempt := 1; ; attempt++ {
		httpReq, err := make()
		if err != nil {
			return nil, err
		}
		respBody, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return nil, sdcppGaveUp(attempt, lastWaking)
			}
			return nil, err
		}
		if status < 300 {
			return respBody, nil
		}
		if !sdcppRetryable(status, respBody) {
			return nil, fmt.Errorf("the image engine's %s answered %d %s: %s",
				what, status, http.StatusText(status), sdcppErrText(respBody))
		}
		lastWaking = sdcppErrText(respBody)
		select {
		case <-ctx.Done():
			return nil, sdcppGaveUp(attempt, lastWaking)
		case <-time.After(retryAfter):
		}
	}
}

// comfyWarnings is what this route knows it cannot honour, mirroring sdcppWarnings.
func comfyWarnings(req Request) []string {
	var out []string
	if b := strings.ToLower(strings.TrimSpace(req.Background)); b == "transparent" {
		out = append(out, "background=transparent requested, opaque produced (this engine's checkpoints have no alpha channel)")
	}
	return out
}

// comfySeedFor is the seed this request samples from: the caller's, when they pinned one, and a
// fresh random one otherwise.
//
// A pinned seed is what makes two requests comparable, which is the only way to show that one
// changed thing — a LoRA, a checkpoint — is what changed the picture (ADR 0072 phase P3). The
// default stays random because that is what a caller who says nothing means, and because of the
// cache below.
func comfySeedFor(req Request) (int64, error) {
	if req.Seed != nil {
		return *req.Seed, nil
	}
	return comfyRandomSeed()
}

// comfyRandomSeed picks a fresh seed for a request that pinned none. ComfyUI caches a node's
// output by its inputs (measured, bench-image-engine.py), so replaying an identical graph answers
// the second call from cache in half a second rather than generating anything — which is the
// RIGHT answer for a caller who pinned a seed and is asking for the same picture, and a confusing
// one for a caller who did not. comfyCacheWarning says which of the two happened.
func comfyRandomSeed() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("could not pick a seed: %w", err)
	}
	n := int64(binary.BigEndian.Uint64(b[:]) & math.MaxInt64)
	return n, nil
}

// submit is POST /prompt, through the same retry-on-503-engine_waking loop as sdcpp.go's send.
// For a plain generate it is the first call of the three and therefore the one that meets a
// stopped engine; for edit and inpaint the uploads got there first, which is exactly why they
// share this loop rather than each having their own.
func (p *comfyProvider) submit(ctx context.Context, conn EngineConn, graph comfyGraph, model string) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": graph, "client_id": "af-agent"})
	if err != nil {
		return "", err
	}
	respBody, err := p.sendWithWake(ctx, conn, "/prompt", func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sdcppURL(conn, "/prompt"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		// Declares which checkpoint this request used, for the gateway's warm-model tracking
		// (ADR 0072 decision 7) — ComfyUI's /prompt answer carries only a queue id, never a
		// model name, so this header is the only way the CP learns what became warm.
		httpReq.Header.Set("X-AF-Model", model)
		return httpReq, nil
	})
	if err != nil {
		return "", err
	}
	var doc struct {
		PromptID string `json:"prompt_id"`
		Error    any    `json:"error"`
	}
	if json.Unmarshal(respBody, &doc) != nil || doc.PromptID == "" {
		return "", fmt.Errorf("the image engine's /prompt answer had no prompt_id: %s", tail(string(respBody), 400))
	}
	return doc.PromptID, nil
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
//
// A retryable answer here is waited out exactly as submit waits one out, and the reason is a
// message a caller actually received (ADR 0072 欠落 9): a box swapped out mid-poll made the
// gateway answer `503 engine_waking`, whose own text ends in "retry" — and this loop returned it
// as a failure without retrying anything. Generation is 47-78 seconds cold (measured), so the
// window in which the box can change under a poll is wide open, not theoretical.
//
// What a retry cannot recover is the QUEUE: a restarted ComfyUI holds no history for a prompt id
// the previous process accepted, so once a wake has been seen, a 200 that does not carry this
// prompt means the work is gone. That is reported rather than polled for, because the alternative
// is silence until the request's whole 16-minute budget runs out.
func (p *comfyProvider) awaitHistory(ctx context.Context, conn EngineConn, promptID string) (comfyHistory, error) {
	lastWaking, sawWaking := "", false
	for {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sdcppURL(conn, "/history/"+url.PathEscape(promptID)), nil)
		if err != nil {
			return comfyHistory{}, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		body, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return comfyHistory{}, comfyPollTimedOut(lastWaking, ctx.Err())
			}
			return comfyHistory{}, err
		}
		wait := comfyPollEvery
		switch {
		case status >= 300 && !sdcppRetryable(status, body):
			return comfyHistory{}, fmt.Errorf("the image engine's /history answered %d %s: %s",
				status, http.StatusText(status), sdcppErrText(body))
		case status >= 300:
			lastWaking, sawWaking = sdcppErrText(body), true
			// The gateway's own Retry-After, not the poll interval: it is answering for a box
			// that is being started, and asking every second only adds requests to a wake.
			wait = retryAfter
		default:
			var byID map[string]comfyHistory
			if err := json.Unmarshal(body, &byID); err != nil {
				return comfyHistory{}, fmt.Errorf("the image engine's /history answer was not JSON: %w", err)
			}
			hist, known := byID[promptID]
			if known {
				if hist.Status.StatusStr == "error" {
					return comfyHistory{}, fmt.Errorf("the image engine failed the request: %s", comfyErrorMessages(hist))
				}
				if hist.Status.Completed {
					return hist, nil
				}
			}
			// An UNKNOWN prompt id is normal while the picture is being made — ComfyUI's history
			// holds finished prompts only, and the queued one lives in /queue. It stops being
			// normal once this engine has restarted under us.
			if !known && sawWaking {
				return comfyHistory{}, fmt.Errorf(
					"the image engine restarted while this picture was being made, and the queued request did not survive it (%s)"+
						" — ask again; nothing was generated", lastWaking)
			}
		}
		select {
		case <-ctx.Done():
			return comfyHistory{}, comfyPollTimedOut(lastWaking, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// comfyPollTimedOut names the engine's own last word when the wait ran out during a wake, so a
// timeout that happened BECAUSE the box was being replaced does not read as a stalled generation.
func comfyPollTimedOut(lastWaking string, err error) error {
	if lastWaking == "" {
		return fmt.Errorf("waiting for the image engine timed out: %w", err)
	}
	return fmt.Errorf("waiting for the image engine timed out while it was still starting (%s): %w", lastWaking, err)
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

// comfySaveNode is the id every template gives its SaveImage node. It is the graph's terminal
// output, so "was this node cached" is the same question as "was any picture made at all".
const comfySaveNode = "save"

// comfyCacheWarning says, only when it actually happened, that the engine returned a picture it
// already had instead of generating one.
//
// The signal is ComfyUI's OWN `execution_cached` status message, which lists the node ids it
// skipped (execution.py, v0.34.0) and rides in /history's status.messages — the same field the
// error path already reads. So this is a fact the engine reported, not a guess from a suspiciously
// short elapsed time.
//
// Why warn at all, given that a repeat of an identical seeded request SHOULD return the identical
// picture: because the two readings of a half-second answer are opposite. A caller comparing
// "with the LoRA" against "without" wants to know nothing was recomputed if the graphs happened to
// match; a caller who changed something the graph does not carry (ADR 0069 has no negative prompt,
// no steps, no cfg) would otherwise conclude the engine ignored a change that never reached it.
// Silent when nothing was cached, which is every first call — so it costs the common path nothing.
func comfyCacheWarning(hist comfyHistory) string {
	for _, m := range hist.Status.Messages {
		pair, ok := m.([]any)
		if !ok || len(pair) < 2 {
			continue
		}
		if event, _ := pair[0].(string); event != "execution_cached" {
			continue
		}
		data, ok := pair[1].(map[string]any)
		if !ok {
			continue
		}
		nodes, _ := data["nodes"].([]any)
		for _, n := range nodes {
			if id, _ := n.(string); id == comfySaveNode {
				return "this picture came from the engine's cache, not from a new generation — " +
					"the graph was identical to one it had already run (same seed, prompt, size and model). " +
					"Anything you changed that is not one of those does not reach this route"
			}
		}
	}
	return ""
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
