package imagegen

// What is pinned here is the WIRE: the OpenAI Images API is the contract this code was written
// against (upstream examples/server/api.md, read 2026-09-07, against sd-server's implementation
// of it — ADR 0071 P1), and the two shapes it uses are not the same — generations is JSON, edits
// is multipart/form-data with the mask as a file part. ADR 0083 P1 generalised the client beyond
// that one engine; these tests exercise it against a fake server standing in for "any
// OpenAI-compatible server", not specifically sd-server.

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
	"time"
)

// openaiCompatStub stands in for the CP's engine gateway. It records what arrived and answers
// with the OpenAI-compatible body a real server produces.
func openaiCompatStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *openaiCompatProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return &openaiCompatProvider{
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

// openaiCompatAnswer is the response body: data[].b64_json, exactly as measured on the GPU box.
func openaiCompatAnswer(t *testing.T, images ...[]byte) string {
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

func TestOpenAICompatGenerateSendsTheOpenAIShapeAndDecodesTheAnswer(t *testing.T) {
	var gotPath, gotAuth, gotCType, gotModelHeader string
	var gotBody map[string]any
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotCType = r.Header.Get("Content-Type")
		gotModelHeader = r.Header.Get("X-AF-Model")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 64, 32), tinyPNG(t, 64, 32)))
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
	// The row declared exactly one model, so it is the resolved default and rides in the body
	// (ADR 0083 decision 2: the row's declaration is the switch).
	if gotBody["model"] != "sdxl-base-1.0" {
		t.Errorf("model = %v, want the row's declared checkpoint", gotBody["model"])
	}
	if gotBody["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json requested explicitly", gotBody["response_format"])
	}
	// X-AF-Model IS sent, on the header rather than the OpenAI-shaped body — it is what lets
	// the gateway's warm-model tracking (ADR 0072 decision 7) work for the image role at all,
	// since the server's own answer carries pixels, never a model name.
	if gotModelHeader != "sdxl-base-1.0" {
		t.Errorf("X-AF-Model = %q, want the engine's started-with checkpoint", gotModelHeader)
	}
	if len(res.Images) != 2 {
		t.Fatalf("got %d images, want 2", len(res.Images))
	}
	if res.Images[0].Width != 64 || res.Images[0].Height != 32 {
		t.Errorf("dimensions = %dx%d, want 64x32 — they come from the bytes, not from the request",
			res.Images[0].Width, res.Images[0].Height)
	}
	if res.Provider != ProviderOpenAICompat || res.Model != "sdxl-base-1.0" {
		t.Errorf("provenance = %s/%s", res.Provider, res.Model)
	}
	if res.Destination == "" {
		t.Error("no destination: the provider id alone does not say where the prompt went")
	}
	// There is no driver model on this route, so there are no tokens — "none", not zero.
	if res.Usage.Measured {
		t.Error("usage claimed to be measured on a route with no tokens at all")
	}
	if res.CostUSD != 0 {
		t.Errorf("cost = %v — an engine hour is a component cost, not a per-image price", res.CostUSD)
	}
}

// A row that declares no model at all gets no `model` field — the same behaviour this route has
// always had for a single-checkpoint server that never told the catalogue its own id.
func TestOpenAICompatSendsNoModelWhenTheRowDeclaresNone(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	}))
	t.Cleanup(srv.Close)
	p := &openaiCompatProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			return EngineConn{BaseURL: srv.URL + "/engine/image/v1", Token: "t"}, true
		},
	}
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, sent := gotBody["model"]; sent {
		t.Errorf("model = %v, want no model field when the row declares none", gotBody["model"])
	}
}

// The caller's own choice of model outranks the row's first declared id.
func TestOpenAICompatCallerModelOverridesTheRowDefault(t *testing.T) {
	var gotBody map[string]any
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	})
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x", Model: "other-checkpoint"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotBody["model"] != "other-checkpoint" {
		t.Errorf("model = %v, want the caller's own choice", gotBody["model"])
	}
}

// A url-only answer is REFUSED, not fetched: response_format=b64_json was requested, so a server
// that ignores it and returns a link is a server this client does not follow (ADR 0083 decision
// 2 — the egress is not this provider's to make on the caller's behalf).
func TestOpenAICompatRefusesAUrlOnlyAnswer(t *testing.T) {
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		out, _ := json.Marshal(map[string]any{
			"created": 1,
			"data":    []map[string]string{{"url": "https://example.invalid/generated.png"}},
		})
		_, _ = w.Write(out)
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"})
	if err == nil {
		t.Fatal("a url-only answer was accepted")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("err = %v, want the field name named", err)
	}
}

// "auto" and an empty size are not sizes: they are omitted so the checkpoint's own default
// applies, rather than being passed through as a literal the engine would reject.
func TestOpenAICompatOmitsANonSize(t *testing.T) {
	for _, size := range []string{"", "auto", "big"} {
		var gotBody map[string]any
		p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
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
func TestOpenAICompatInpaintPostsMultipartWithTheMask(t *testing.T) {
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
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
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
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 16, 16)))
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
	if fields["model"] != "sdxl-base-1.0" {
		t.Errorf("model field = %q, want the row's declared checkpoint", fields["model"])
	}
	if fields["response_format"] != "b64_json" {
		t.Errorf("response_format field = %q, want b64_json", fields["response_format"])
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
func TestOpenAICompatRefusesTheImpossibleCombinations(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if err := os.WriteFile(src, tinyPNG(t, 8, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
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

// Caps is per (provider, MODEL) — ADR 0069 decision 5 — and sizes come only from what the row
// declares (ADR 0083 decision 2): no guess from the model id survives the rename, because a
// guess is a claim about Stable Diffusion checkpoints specifically and this client no longer
// assumes the far end is one.
func TestOpenAICompatCapsSizesComeOnlyFromTheRow(t *testing.T) {
	p := &openaiCompatProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{
			BaseURL: "http://x/v1", Token: "t",
			Models: []string{"sdxl-base-1.0"},
			Sizes:  map[string][]string{"sdxl-base-1.0": {"1024x1024", "1152x896"}},
		}, true
	}}
	if got := p.Caps("").Sizes; len(got) != 2 || got[0] != "1024x1024" {
		t.Errorf("sizes = %v, want the row's own declaration", got)
	}
	// A model the row says nothing about offers no sizes at all — not a guess from its id.
	if got := p.Caps("undeclared-checkpoint").Sizes; len(got) != 0 {
		t.Errorf("sizes = %v, want none for an undeclared model", got)
	}
	caps := p.Caps("")
	for _, op := range []Op{OpGenerate, OpEdit, OpInpaint} {
		if !caps.Supports(op) {
			t.Errorf("Caps does not claim %s", op)
		}
	}
	// This client cannot verify whether an arbitrary server's checkpoints have an alpha
	// channel, so it advertises none rather than guessing either way.
	if len(caps.Backgrounds) != 0 {
		t.Errorf("Backgrounds = %v, want none", caps.Backgrounds)
	}
}

// Asleep is the NORMAL state of a managed engine and Ready must not mean "up" — reporting it as
// unready would take generate_image out of tools/list for exactly the reason the on-demand
// design exists to make invisible. Ready means "this deployment has a row, and we hold a token".
func TestOpenAICompatReadyIsAboutConfigurationNotWakefulness(t *testing.T) {
	if (&openaiCompatProvider{}).Ready(context.Background()) {
		t.Error("ready with no lookup at all")
	}
	none := &openaiCompatProvider{lookup: func(context.Context) (EngineConn, bool) { return EngineConn{}, false }}
	if none.Ready(context.Background()) {
		t.Error("ready with no engine in the deployment")
	}
	half := &openaiCompatProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://x/v1"}, true // no token
	}}
	if half.Ready(context.Background()) {
		t.Error("ready with no token — every call would be a 401")
	}
	// Nothing was dialled to answer any of that: an engine that costs $1.26/hour must not be
	// woken by a tools/list.
	full := &openaiCompatProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://127.0.0.1:1/v1", Token: "t"}, true
	}}
	if !full.Ready(context.Background()) {
		t.Error("not ready with an engine configured (was the address dialled?)")
	}
}

// The gateway's own refusal is the informative one — it is the only party that knows the box
// did not come up — so it has to survive the trip to the model rather than being replaced by
// a status code.
func TestOpenAICompatPassesTheGatewaysReasonThrough(t *testing.T) {
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"engine_unavailable","message":"the fleet's own inference engine did not come up in time: gave up after 15m0s"}}`)
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not come up in time") {
		t.Fatalf("err = %v, want the gateway's own message", err)
	}
}

// Decision 7 (ADR 0072): what could not be honoured is reported, never silently dropped.
func TestOpenAICompatReportsATransparentBackgroundItCannotDo(t *testing.T) {
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x", Background: "transparent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "transparent") {
		t.Fatalf("warnings = %v — a request this route cannot meet came back silent", res.Warnings)
	}
}

func TestOpenAICompatRejectsAnAnswerItCannotRead(t *testing.T) {
	for _, body := range []string{`not json`, `{"data":[]}`, `{"data":[{"b64_json":"@@@"}]}`} {
		p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"}); err == nil {
			t.Errorf("%s: accepted", body)
		}
	}
}

// "auto" walks the effective order, and the fleet's own engines come first where they exist:
// the other routes spend a MEMBER's plan quota, while a deployment that stood up this engine
// already decided to pay for the hardware itself. Naming a provider explicitly still pins it,
// and a deployment with no engine — the normal case — is unaffected.
func TestAutoPrefersTheSelfHostedEngineOverAMembersPlan(t *testing.T) {
	caps := func(id string) Caps {
		if id == ProviderOpenAICompat {
			return Caps{Ops: []Op{OpGenerate, OpEdit, OpInpaint}}
		}
		return Caps{Ops: []Op{OpGenerate, OpEdit}}
	}
	ready := map[string]bool{ProviderOpenAICompat: true, ProviderCodex: true, ProviderAgy: true}
	got := chooseImageProviders("", Request{Op: OpGenerate}, providerOrder, ready, caps)
	if len(got) == 0 || got[0] != ProviderOpenAICompat {
		t.Errorf("auto chose %v, want %q first", got, ProviderOpenAICompat)
	}
	// The fall-through order still holds behind it: an engine that turns out to be broken must
	// leave the member's own routes reachable rather than ending the call.
	if len(got) != 3 {
		t.Errorf("auto returned %v, want every ready provider in order", got)
	}
	if got := chooseImageProviders(ProviderCodex, Request{Op: OpGenerate}, providerOrder, ready, caps); len(got) != 1 || got[0] != ProviderCodex {
		t.Errorf("an explicit codex became %v", got)
	}
	// With no engine deployed — the normal case — auto is exactly what it was before.
	if got := chooseImageProviders("", Request{Op: OpGenerate}, providerOrder,
		map[string]bool{ProviderCodex: true, ProviderAgy: true}, caps); len(got) == 0 || got[0] != ProviderAgy {
		t.Errorf("with no engine, auto chose %v", got)
	}
	// inpaint is the fleet engine's alone here, so the list must not offer a provider that
	// cannot do it — a fall-through to one would be a second failure, not a rescue.
	if got := chooseImageProviders("", Request{Op: OpInpaint}, providerOrder, ready, caps); len(got) != 1 || got[0] != ProviderOpenAICompat {
		t.Errorf("inpaint chose %v", got)
	}
}

// --- waking the engine across more than one request (ADR 0071 P1, measured 2026-09-07) -----

// shortRetries makes the wait between attempts a mechanism a test can observe rather than a
// wall-clock cost. Without it these tests would spend the real three-second floor per attempt.
func shortRetries(t *testing.T) {
	t.Helper()
	oldMin, oldMax := sdcppRetryMin, sdcppRetryMax
	sdcppRetryMin, sdcppRetryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { sdcppRetryMin, sdcppRetryMax = oldMin, oldMax })
}

// The claim the whole retry loop exists for. The gateway cannot hold a non-streaming request
// while a GPU box boots — the ingress closes an idle connection first (measured: the CP
// answered `503 59.998s` against its own 900-second hold, while the engine took 165 s) — so
// one tool call has to survive being answered "not yet" several times.
func TestOpenAICompatKeepsAskingWhileTheEngineIsWaking(t *testing.T) {
	shortRetries(t)
	var attempts int
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "6")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"engine_waking","message":"the fleet's own inference engine is starting; retry"}}`)
			return
		}
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"})
	if err != nil {
		t.Fatalf("Generate: %v — a waking engine must not end the call", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d", len(res.Images))
	}
}

// An ingress that gave up on the gateway's behalf. It never gets to say `engine_waking`, so
// the status is all there is — and it is also what a Control Plane too old to fold its wait
// looks like from here.
func TestOpenAICompatRetriesAnIngressTimeout(t *testing.T) {
	shortRetries(t)
	var attempts int
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, "<html>504 Gateway Time-out</html>")
			return
		}
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	})
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want the 504 to have been retried once", attempts)
	}
}

// The two 503s that are refusals, not delays. Retrying either would turn a clear answer into
// a sixteen-minute hang, with the model told nothing until the very end.
func TestOpenAICompatDoesNotRetryARefusal(t *testing.T) {
	shortRetries(t)
	for _, tc := range []struct{ code, want string }{
		{"engine_off", "switched off"},
		{"engine_unavailable", "did not come up in time"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var attempts int
			p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"code":"`+tc.code+`","message":"`+tc.want+`"}}`)
			})
			_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "x"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want the gateway's own message", err)
			}
			if attempts != 1 {
				t.Errorf("attempts = %d, want 1 — a refusal is not a delay", attempts)
			}
		})
	}
}

// The retry has to REBUILD the request, not replay it: an edit's body is a multipart document
// that the first attempt has already read to the end. A retried POST carrying an empty body
// would fail on the one endpoint in this system that is not JSON, and only there.
func TestOpenAICompatRebuildsTheMultipartBodyOnRetry(t *testing.T) {
	shortRetries(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	if err := os.WriteFile(src, tinyPNG(t, 16, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	var attempts int
	sizes := map[int]int{}
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("attempt %d content-type: %v", attempts, err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				break
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "image" {
				sizes[attempts] = len(b)
			}
		}
		if attempts == 1 {
			w.Header().Set("Retry-After", "6")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"engine_waking","message":"starting"}}`)
			return
		}
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 16, 16)))
	})
	if _, err := p.Generate(context.Background(), Request{
		Op: OpEdit, Prompt: "red", Inputs: []string{src},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if sizes[2] == 0 || sizes[2] != sizes[1] {
		t.Errorf("image part was %d bytes on the retry and %d on the first try — the body was replayed, not rebuilt",
			sizes[2], sizes[1])
	}
}

// The budget is the caller's, not the gateway's: when it runs out the message has to say what
// was being waited for, because "context deadline exceeded" after fifteen minutes of waking a
// GPU box tells nobody what to do next.
func TestOpenAICompatGivesUpWithAReasonWhenTheBudgetRunsOut(t *testing.T) {
	shortRetries(t)
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"engine_waking","message":"starting"}}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := p.Generate(ctx, Request{Op: OpGenerate, Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not come up within") {
		t.Fatalf("err = %v, want a reason naming the wait", err)
	}
}

// The other way the budget can run out: INSIDE a request rather than between two. Which of
// the two notices first is a race — it showed up as a flaky test before it showed up as a
// thought — and a caller who waited a quarter of an hour must not be told
// "context deadline exceeded" just because the clock happened to land mid-flight.
func TestOpenAICompatGivesUpWithAReasonWhenTheBudgetRunsOutMidRequest(t *testing.T) {
	shortRetries(t)
	var attempts int
	p := openaiCompatStub(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"engine_waking","message":"still coming up"}}`)
			return
		}
		// Slower than the budget, but BOUNDED. A handler that waits to be released — on a
		// channel or on the request's own context — hangs the test instead: httptest's Close
		// waits for outstanding handlers and is registered before anything this function can
		// add, so it runs last (measured, as a 600-second hang).
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, openaiCompatAnswer(t, tinyPNG(t, 8, 8)))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := p.Generate(ctx, Request{Op: OpGenerate, Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "did not come up within") {
		t.Fatalf("err = %v, want the same reason as when the wait ends between attempts", err)
	}
	// And it still carries what the gateway last said, which is the only actionable half.
	if !strings.Contains(err.Error(), "still coming up") {
		t.Errorf("err = %v, want the gateway's last word kept", err)
	}
}

func TestOpenAICompatRetryAfterIsClamped(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", sdcppRetryMin},
		{"nonsense", sdcppRetryMin},
		{"0", sdcppRetryMin},
		{"1", sdcppRetryMin},
		{"6", 6 * time.Second},
		{"3600", sdcppRetryMax},
	} {
		if got := sdcppRetryAfter(tc.in); got != tc.want {
			t.Errorf("sdcppRetryAfter(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
