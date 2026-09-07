package imagegen

// The self-hosted image engine's provider (ADR 0071 P1). What is pinned here is the WIRE:
// sd-server's OpenAI-compatible face is the contract this code was written against
// (upstream examples/server/api.md, read 2026-09-07), and the two shapes it uses are not the
// same — generations is JSON, edits is multipart/form-data with the mask as a file part.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdcppStub stands in for the CP's engine gateway. It records what arrived and answers with
// the OpenAI-compatible body sd-server produces.
func sdcppStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *sdcppProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return &sdcppProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			return EngineConn{
				BaseURL: srv.URL + "/engine/image/v1",
				Token:   "afe_test",
				Models:  []string{"sdxl-base-1.0"},
			}, true
		},
	}
}

// sdcppAnswer is the response body: data[].b64_json, exactly as measured on the GPU box.
func sdcppAnswer(t *testing.T, images ...[]byte) string {
	t.Helper()
	data := make([]map[string]string, 0, len(images))
	for _, b := range images {
		data = append(data, map[string]string{"b64_json": base64.StdEncoding.EncodeToString(b)})
	}
	out, err := json.Marshal(map[string]any{"created": 1, "output_format": "png", "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSdcppGenerateSendsTheOpenAIShapeAndDecodesTheAnswer(t *testing.T) {
	var gotPath, gotAuth, gotCType string
	var gotBody map[string]any
	p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotCType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, sdcppAnswer(t, tinyPNG(t, 64, 32), tinyPNG(t, 64, 32)))
	})

	res, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a red square", Size: "1024x1024", Count: 2,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotPath != "/engine/image/v1/images/generations" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer afe_test" {
		t.Errorf("authorization = %q — the engine token is what the gateway authenticates", gotAuth)
	}
	if gotCType != "application/json" {
		t.Errorf("content-type = %q", gotCType)
	}
	if gotBody["prompt"] != "a red square" || gotBody["size"] != "1024x1024" || gotBody["n"] != float64(2) {
		t.Errorf("body = %v", gotBody)
	}
	// `model` is deliberately not sent: sd-server holds one checkpoint, chosen by a startup
	// flag, and a model id on the request would suggest it could be switched.
	if _, sent := gotBody["model"]; sent {
		t.Error("a model id was sent to an engine that holds exactly one checkpoint")
	}
	if len(res.Images) != 2 {
		t.Fatalf("got %d images, want 2", len(res.Images))
	}
	if res.Images[0].Width != 64 || res.Images[0].Height != 32 {
		t.Errorf("dimensions = %dx%d, want 64x32 — they come from the bytes, not from the request",
			res.Images[0].Width, res.Images[0].Height)
	}
	if res.Provider != ProviderSdcpp || res.Model != "sdxl-base-1.0" {
		t.Errorf("provenance = %s/%s", res.Provider, res.Model)
	}
	if res.Destination == "" {
		t.Error("no destination: `sdcpp` alone does not tell a reader the prompt went to the fleet's own box")
	}
	// There is no driver model on this route, so there are no tokens — "none", not zero.
	if res.Usage.Measured {
		t.Error("usage claimed to be measured on a route with no tokens at all")
	}
	if res.CostUSD != 0 {
		t.Errorf("cost = %v — an engine hour is a component cost, not a per-image price", res.CostUSD)
	}
}

// "auto" and an empty size are not sizes: they are omitted so the checkpoint's own default
// applies, rather than being passed through as a literal the engine would reject.
func TestSdcppOmitsANonSize(t *testing.T) {
	for _, size := range []string{"", "auto", "big"} {
		var gotBody map[string]any
		p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_, _ = io.WriteString(w, sdcppAnswer(t, tinyPNG(t, 8, 8)))
		})
		if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x", Size: size}); err != nil {
			t.Fatalf("size %q: %v", size, err)
		}
		if _, sent := gotBody["size"]; sent {
			t.Errorf("size %q was forwarded as %v", size, gotBody["size"])
		}
	}
}

// The edits endpoint is multipart/form-data, and the mask is a FILE part. This is the one
// request in the system that is not JSON — the reason the CP's gateway forwards the caller's
// Content-Type instead of stamping application/json on everything (the boundary is in it).
func TestSdcppInpaintPostsMultipartWithTheMask(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	mask := filepath.Join(dir, "mask.png")
	if err := os.WriteFile(src, tinyPNG(t, 16, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mask, tinyPNG(t, 16, 16), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotPath string
	files := map[string]int{}
	fields := map[string]string{}
	p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content-type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			if part.FileName() != "" {
				files[part.FormName()] = len(b)
			} else {
				fields[part.FormName()] = string(b)
			}
		}
		_, _ = io.WriteString(w, sdcppAnswer(t, tinyPNG(t, 16, 16)))
	})

	if _, err := p.Generate(context.Background(), Request{
		Op: OpInpaint, Prompt: "a blue circle", Size: "512x512", Inputs: []string{src}, Mask: mask,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotPath != "/engine/image/v1/images/edits" {
		t.Errorf("path = %q — inpaint is edits with a mask, not its own endpoint", gotPath)
	}
	if fields["prompt"] != "a blue circle" || fields["size"] != "512x512" {
		t.Errorf("fields = %v", fields)
	}
	if files["image"] == 0 {
		t.Error("no image part")
	}
	if files["mask"] == 0 {
		t.Error("no mask part — an inpaint without one repaints the whole picture")
	}
}

// A mask handed to a route that cannot use it produces a picture OF the mask, so the refusal
// is here rather than at the engine. The mirror case matters as much: inpaint IS the mask.
func TestSdcppRefusesTheImpossibleCombinations(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if err := os.WriteFile(src, tinyPNG(t, 8, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, sdcppAnswer(t, tinyPNG(t, 8, 8)))
	})
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"inpaint with no mask", Request{Op: OpInpaint, Prompt: "x", Inputs: []string{src}}},
		{"edit with no input image", Request{Op: OpEdit, Prompt: "x"}},
		{"an op this engine does not have", Request{Op: OpUpscale, Prompt: "x"}},
		{"no prompt", Request{Op: OpGenerate}},
		{"more inputs than Caps promises", Request{Op: OpEdit, Prompt: "x", Inputs: []string{src, src}}},
	} {
		if _, err := p.Generate(context.Background(), tc.req); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
	if called {
		t.Error("a refused request still reached the engine — which would have woken a GPU box")
	}
}

// Caps is per (provider, MODEL) — ADR 0069 decision 5 — and for this provider the model is
// the checkpoint the stack staged. SDXL and SD 1.5 are the same code path and differ only in
// what sizes produce a picture rather than a smear.
func TestSdcppCapsFollowTheCheckpoint(t *testing.T) {
	p := &sdcppProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://x/v1", Token: "t", Models: []string{"sdxl-base-1.0"}}, true
	}}
	if got := p.Caps("").Sizes; got[0] != "1024x1024" {
		t.Errorf("the default model's sizes = %v, want SDXL's 1024 first", got)
	}
	if got := p.Caps("sd-v1-5").Sizes; got[0] != "512x512" {
		t.Errorf("sd-v1-5 sizes = %v, want 512 first", got)
	}
	if got := p.Caps("something-else").Sizes; len(got) == 0 {
		t.Error("an unrecognised checkpoint reported no sizes at all")
	}
	caps := p.Caps("")
	for _, op := range []Op{OpGenerate, OpEdit, OpInpaint} {
		if !caps.Supports(op) {
			t.Errorf("Caps does not claim %s", op)
		}
	}
	// No Stable Diffusion checkpoint here produces an alpha channel, so the honest answer is
	// an empty list plus a warning at generation time — not a background this route cannot do.
	if len(caps.Backgrounds) != 0 {
		t.Errorf("Backgrounds = %v, want none", caps.Backgrounds)
	}
}

// Asleep is the NORMAL state of this engine and Ready must not mean "up" — reporting it as
// unready would take generate_image out of tools/list for exactly the reason the on-demand
// design exists to make invisible. Ready means "this deployment has one, and we hold a token".
func TestSdcppReadyIsAboutConfigurationNotWakefulness(t *testing.T) {
	if (&sdcppProvider{}).Ready(context.Background()) {
		t.Error("ready with no lookup at all")
	}
	none := &sdcppProvider{lookup: func(context.Context) (EngineConn, bool) { return EngineConn{}, false }}
	if none.Ready(context.Background()) {
		t.Error("ready with no engine in the deployment")
	}
	half := &sdcppProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://x/v1"}, true // no token
	}}
	if half.Ready(context.Background()) {
		t.Error("ready with no token — every call would be a 401")
	}
	// Nothing was dialled to answer any of that: an engine that costs $1.26/hour must not be
	// woken by a tools/list.
	full := &sdcppProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://127.0.0.1:1/v1", Token: "t"}, true
	}}
	if !full.Ready(context.Background()) {
		t.Error("not ready with an engine configured (was the address dialled?)")
	}
}

// The gateway's own refusal is the informative one — it is the only party that knows the box
// did not come up — so it has to survive the trip to the model rather than being replaced by
// a status code.
func TestSdcppPassesTheGatewaysReasonThrough(t *testing.T) {
	p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"engine_unavailable","message":"the fleet's own inference engine did not come up in time: gave up after 15m0s"}}`)
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not come up in time") {
		t.Fatalf("err = %v, want the gateway's own message", err)
	}
}

// Decision 7: what could not be honoured is reported, never silently dropped.
func TestSdcppReportsATransparentBackgroundItCannotDo(t *testing.T) {
	p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, sdcppAnswer(t, tinyPNG(t, 8, 8)))
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x", Background: "transparent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "transparent") {
		t.Fatalf("warnings = %v — a request this route cannot meet came back silent", res.Warnings)
	}
}

func TestSdcppRejectsAnAnswerItCannotRead(t *testing.T) {
	for _, body := range []string{`not json`, `{"data":[]}`, `{"data":[{"b64_json":"@@@"}]}`} {
		p := sdcppStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"}); err == nil {
			t.Errorf("%s: accepted", body)
		}
	}
}

// "auto" walks providerOrder, and the fleet's own engine comes first: the Codex route spends
// the USER's ChatGPT plan quota, while a deployment that stood up this engine already decided
// to pay for the hardware itself. Naming codex explicitly still picks it.
func TestAutoPrefersTheSelfHostedEngineOverTheUsersPlan(t *testing.T) {
	caps := func(id string) Caps {
		if id == ProviderSdcpp {
			return Caps{Ops: []Op{OpGenerate, OpEdit, OpInpaint}}
		}
		return Caps{Ops: []Op{OpGenerate, OpEdit}}
	}
	ready := map[string]bool{ProviderSdcpp: true, ProviderCodex: true}
	if got := chooseImageProvider("", Request{Op: OpGenerate}, providerOrder, ready, caps); got != ProviderSdcpp {
		t.Errorf("auto chose %q, want %q", got, ProviderSdcpp)
	}
	if got := chooseImageProvider(ProviderCodex, Request{Op: OpGenerate}, providerOrder, ready, caps); got != ProviderCodex {
		t.Errorf("an explicit codex became %q", got)
	}
	// And with no engine deployed — the normal case — auto still finds the Codex route.
	if got := chooseImageProvider("", Request{Op: OpGenerate}, providerOrder,
		map[string]bool{ProviderCodex: true}, caps); got != ProviderCodex {
		t.Errorf("with no engine, auto chose %q", got)
	}
}
