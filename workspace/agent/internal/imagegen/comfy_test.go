package imagegen

// comfy_test.go — the WIRE this provider was written against: POST /prompt returns a queue id,
// GET /history/<id> is polled until done, GET /view returns the raw bytes (ADR 0072 decision 4,
// phase P2). What is pinned here is the three-call sequence and the retry-on-503 behaviour
// ported from sdcpp; comfy_workflows_test.go pins the graphs themselves.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// comfyStub stands in for the CP's engine gateway, answering the three-call sequence with a
// single fixed image. conn lets each test declare its own catalogue shape (models, files,
// baseModel, warm).
func comfyStub(t *testing.T, conn EngineConn, promptHandler func(w http.ResponseWriter, r *http.Request, body map[string]any)) (*comfyProvider, *httptest.Server) {
	t.Helper()
	const promptID = "af-test-prompt"
	png := tinyPNG(t, 2, 3)
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if promptHandler != nil {
			promptHandler(w, r, body)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			promptID: map[string]any{
				"status": map[string]any{"completed": true, "status_str": "success"},
				"outputs": map[string]any{
					"save": map[string]any{"images": []map[string]any{
						{"filename": "af-sdxl_00001_.png", "subfolder": "", "type": "output"},
					}},
				},
			},
		})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if conn.BaseURL == "" {
		conn.BaseURL = srv.URL + "/engine/image/v1"
	}
	if conn.Token == "" {
		conn.Token = "afe_test"
	}
	p := &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) { return conn, true },
	}
	return p, srv
}

// comfyEditStub is comfyStub plus POST /upload/image, which is the call an edit or an inpaint
// makes before the graph can name the picture at all.
func comfyEditStub(t *testing.T, conn EngineConn, uploadHandler http.HandlerFunc, promptHandler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *comfyProvider {
	t.Helper()
	p, srv := comfyStub(t, conn, promptHandler)
	srv.Config.Handler.(*http.ServeMux).HandleFunc("/engine/image/v1/upload/image", uploadHandler)
	return p
}

func sdxlConn() EngineConn {
	return EngineConn{
		Models:    []string{"sdxl-base-1.0"},
		BaseModel: map[string]string{"sdxl-base-1.0": "sdxl"},
		Files: map[string][]EngineFile{
			"sdxl-base-1.0": {{Name: "sd_xl_base_1.0.safetensors"}},
		},
	}
}

func TestComfyGenerateRunsThePromptHistoryViewSequence(t *testing.T) {
	var gotAuth, gotModelHeader string
	var gotGraph map[string]any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotAuth = r.Header.Get("Authorization")
		gotModelHeader = r.Header.Get("X-AF-Model")
		gotGraph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})

	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if gotAuth != "Bearer afe_test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotModelHeader != "sdxl-base-1.0" {
		t.Errorf("X-AF-Model = %q, want the resolved model (the gateway's warm-model tracking reads this, ADR 0072 decision 7)", gotModelHeader)
	}
	if _, ok := gotGraph["ckpt"]; !ok {
		t.Errorf("the graph sent to /prompt has no ckpt node: %v", gotGraph)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(res.Images))
	}
	if res.Images[0].Width != 2 || res.Images[0].Height != 3 {
		t.Errorf("dimensions = %dx%d, want 2x3 (from the real PNG /view answered)", res.Images[0].Width, res.Images[0].Height)
	}
	if res.Provider != ProviderComfy || res.Model != "sdxl-base-1.0" {
		t.Errorf("provider/model = %s/%s", res.Provider, res.Model)
	}
	if res.Usage.Measured {
		t.Error("comfy has no driver model — usage must be Measured: false, like sdcpp")
	}
}

// A request naming no model gets whatever the Control Plane last saw warm (ADR 0072 decision 7),
// not merely the first declared one.
func TestComfyDefaultModelPrefersWarm(t *testing.T) {
	conn := EngineConn{
		Models:    []string{"sdxl-base-1.0", "klein-4b"},
		BaseModel: map[string]string{"sdxl-base-1.0": "sdxl", "klein-4b": "flux2-klein"},
		Warm:      "klein-4b",
	}
	p, _ := comfyStub(t, conn, nil)
	if got := p.DefaultModel(); got != "klein-4b" {
		t.Errorf("DefaultModel() = %q, want the warm one", got)
	}
}

// With nothing known to be warm, the first declared model is the answer — the same fallback
// sdcpp's DefaultModel uses.
func TestComfyDefaultModelFallsBackToFirstDeclared(t *testing.T) {
	conn := EngineConn{Models: []string{"sdxl-base-1.0", "klein-4b"}}
	p, _ := comfyStub(t, conn, nil)
	if got := p.DefaultModel(); got != "sdxl-base-1.0" {
		t.Errorf("DefaultModel() = %q, want the first declared one", got)
	}
}

// Switching away from the warm checkpoint is reported, in the caller's own terms, as a warning
// rather than left for a slow answer to explain itself.
func TestComfyGenerateWarnsOnASwitch(t *testing.T) {
	conn := EngineConn{
		Models:    []string{"sdxl-base-1.0", "klein-4b"},
		BaseModel: map[string]string{"sdxl-base-1.0": "sdxl", "klein-4b": "flux2-klein"},
		Files: map[string][]EngineFile{
			"sdxl-base-1.0": {{Name: "sd_xl_base_1.0.safetensors"}},
		},
		Warm: "klein-4b",
	}
	p, _ := comfyStub(t, conn, nil)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "switching") && strings.Contains(w, "klein-4b") && strings.Contains(w, "sdxl-base-1.0") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a switch warning naming both checkpoints", res.Warnings)
	}
}

// No switch warning when the request already names what is warm — the common case, and the one
// that must stay silent.
func TestComfyGenerateNoSwitchWarningWhenAlreadyWarm(t *testing.T) {
	conn := sdxlConn()
	conn.Warm = "sdxl-base-1.0"
	p, _ := comfyStub(t, conn, nil)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "switching") {
			t.Errorf("unexpected switch warning while already warm: %v", res.Warnings)
		}
	}
}

// The gateway's 503 engine_waking is retried, exactly like sdcpp's own submit loop — this is
// the ONE call of the three that can hit a cold engine.
func TestComfySubmitRetriesOnEngineWaking(t *testing.T) {
	oldMin, oldMax := sdcppRetryMin, sdcppRetryMax
	sdcppRetryMin, sdcppRetryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { sdcppRetryMin, sdcppRetryMax = oldMin, oldMax })

	var attempts int32
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "engine_waking", "message": "starting"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if len(res.Images) != 1 {
		t.Errorf("images = %d, want 1", len(res.Images))
	}
}

// A refusal the gateway does NOT expect to be retried (engine_off, or any other non-2xx) must
// surface as an error, not spin until the context times out.
func TestComfySubmitDoesNotRetryANonRetryableRefusal(t *testing.T) {
	var attempts int32
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "engine_off", "message": "switched off"}})
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want exactly 1 (no retry on a non-retryable refusal)", attempts)
	}
}

// An execution error reported by /history surfaces as a Go error rather than as a successful
// call with zero images.
func TestComfyAwaitHistorySurfacesAnExecutionError(t *testing.T) {
	const promptID = "af-test-prompt"
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			promptID: map[string]any{
				"status": map[string]any{"completed": true, "status_str": "error",
					"messages": []any{[]any{"execution_error", map[string]any{"node_type": "CheckpointLoaderSimple"}}}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			c := sdxlConn()
			c.BaseURL = srv.URL + "/engine/image/v1"
			c.Token = "afe_test"
			return c, true
		},
	}
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "CheckpointLoaderSimple") {
		t.Errorf("error = %v, want it to name the failing node", err)
	}
}

// comfyExecutionError runs one generate against an engine whose /history reports the given
// execution_error payload, and answers with the error the caller is handed.
func comfyExecutionError(t *testing.T, payload map[string]any) error {
	t.Helper()
	const promptID = "af-test-prompt"
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			promptID: map[string]any{
				"status": map[string]any{"completed": true, "status_str": "error",
					"messages": []any{[]any{"execution_error", payload}}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			c := sdxlConn()
			c.BaseURL = srv.URL + "/engine/image/v1"
			c.Token = "afe_test"
			return c, true
		},
	}
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

// The one execution error whose cause a session cannot act on gets a sentence saying what to do
// about it. A checkpoint published with no VAE tensors dies inside ComfyUI on every op, and the
// answer is a Python traceback ending in `VAE is invalid: None` — `generate_image` has no VAE
// argument, so a caller's only reading of that is "retry", and every retry pays the 1-2.5 minute
// checkpoint switch again (measured 2026-09-11 on this deployment).
//
// The traceback is long on purpose: the exception message sorts BEFORE it in ComfyUI's own error
// dict, so this also pins that the hint reads the whole message list rather than the
// 800-character tail the error text shows.
func TestComfyExecutionErrorExplainsACheckpointWithNoVae(t *testing.T) {
	err := comfyExecutionError(t, map[string]any{
		"node_type":         "VAEDecode",
		"exception_message": "ERROR: VAE is invalid: None",
		"traceback":         strings.Repeat("  File \"/ComfyUI/execution.py\", line 306, in execute\n", 40),
	})
	if !strings.Contains(err.Error(), "--vae") {
		t.Errorf("error = %v, want it to name the file the catalogue row is missing", err)
	}
	// The negative control: an unrelated failure must not collect the hint, or it stops meaning
	// anything. It is also proof the check above can fail.
	other := comfyExecutionError(t, map[string]any{
		"node_type": "CheckpointLoaderSimple", "exception_message": "Value not in list: ckpt_name"})
	if strings.Contains(other.Error(), "--vae") {
		t.Errorf("error = %v, want no VAE hint on an unrelated failure", other)
	}
}

// awaitHistory polls until completed=true — a still-running prompt must not be read as done.
func TestComfyAwaitHistoryPollsUntilComplete(t *testing.T) {
	oldPoll := comfyPollEvery
	comfyPollEvery = time.Millisecond
	t.Cleanup(func() { comfyPollEvery = oldPoll })

	const promptID = "af-test-prompt"
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc(fmt.Sprintf("/engine/image/v1/history/%s", promptID), func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		if n < 3 {
			_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{"status": map[string]any{"completed": false}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
			"status": map[string]any{"completed": true, "status_str": "success"},
			"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{
				{"filename": "af-sdxl_00001_.png"}}}},
		}})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG(t, 1, 1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			c := sdxlConn()
			c.BaseURL = srv.URL + "/engine/image/v1"
			c.Token = "afe_test"
			return c, true
		},
	}
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if polls < 3 {
		t.Errorf("polls = %d, want at least 3 (it must not read the first, incomplete answer as done)", polls)
	}
}

// comfyPollStub answers the three-call sequence with a scripted /history: historyHandler decides
// what each poll sees, so a test can make the box go away mid-poll.
func comfyPollStub(t *testing.T, historyHandler http.HandlerFunc) *comfyProvider {
	t.Helper()
	const promptID = "af-test-prompt"
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, historyHandler)
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG(t, 1, 1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			c := sdxlConn()
			c.BaseURL = srv.URL + "/engine/image/v1"
			c.Token = "afe_test"
			return c, true
		},
	}
}

// comfyHistoryDone is the /history answer for a finished prompt.
func comfyHistoryDone(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"af-test-prompt": map[string]any{
		"status":  map[string]any{"completed": true, "status_str": "success"},
		"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{{"filename": "af-sdxl_00001_.png"}}}},
	}})
}

// comfyWaking writes the gateway's own 503 engine_waking — the answer whose message ends in
// "retry".
func comfyWaking(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": "engine_waking", "message": "the fleet's own inference engine is starting; retry"}})
}

// ADR 0072 欠落 9: a box replaced mid-poll made /history answer 503 engine_waking, and the caller
// was handed a message that says "retry" by a loop that did not. It is waited out like /prompt's.
func TestComfyAwaitHistoryRetriesOnEngineWaking(t *testing.T) {
	oldMin, oldMax := sdcppRetryMin, sdcppRetryMax
	sdcppRetryMin, sdcppRetryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { sdcppRetryMin, sdcppRetryMax = oldMin, oldMax })

	var polls int32
	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&polls, 1) < 3 {
			comfyWaking(w)
			return
		}
		comfyHistoryDone(w)
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if polls != 3 {
		t.Errorf("polls = %d, want 3 (two waited-out 503s, then the answer)", polls)
	}
	if len(res.Images) != 1 {
		t.Errorf("images = %d, want 1", len(res.Images))
	}
}

// The other direction, and the reason this is a code check rather than a status check: a 503 the
// gateway does NOT mean to be retried has to surface at once, not fifteen minutes later.
func TestComfyAwaitHistoryDoesNotRetryANonRetryableRefusal(t *testing.T) {
	var polls int32
	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&polls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "engine_off", "message": "switched off"}})
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if polls != 1 {
		t.Errorf("polls = %d, want exactly 1", polls)
	}
	if !strings.Contains(err.Error(), "/history") {
		t.Errorf("error = %v, want it to name the call that refused", err)
	}
}

// Retrying cannot bring a queue back: a restarted ComfyUI holds no history for a prompt the
// previous process accepted. Polling on for the request's whole budget would be silence, so the
// loss is reported as soon as the engine answers again without this prompt.
func TestComfyAwaitHistoryReportsALostQueueAfterARestart(t *testing.T) {
	oldMin, oldMax := sdcppRetryMin, sdcppRetryMax
	oldPoll := comfyPollEvery
	sdcppRetryMin, sdcppRetryMax = time.Millisecond, 5*time.Millisecond
	comfyPollEvery = time.Millisecond
	t.Cleanup(func() {
		sdcppRetryMin, sdcppRetryMax = oldMin, oldMax
		comfyPollEvery = oldPoll
	})

	var polls int32
	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&polls, 1) == 1 {
			comfyWaking(w)
			return
		}
		// The new box is up and knows nothing about the prompt the old one queued.
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := p.Generate(ctx, Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("expected an error rather than a poll that runs out the whole budget")
	}
	if !strings.Contains(err.Error(), "restarted") || !strings.Contains(err.Error(), "ask again") {
		t.Errorf("error = %v, want it to say the engine restarted and the request has to be made again", err)
	}
	if ctx.Err() != nil {
		t.Error("the request's own budget ran out: the loss was polled for rather than reported")
	}
}

// An unknown prompt id on its own is NOT a loss — ComfyUI's history holds finished prompts only,
// so every poll before the picture exists looks exactly like this. The negative control for the
// test above: without the wake, the same answer has to keep polling.
func TestComfyAwaitHistoryKeepsPollingAnUnknownPromptWithoutAWake(t *testing.T) {
	oldPoll := comfyPollEvery
	comfyPollEvery = time.Millisecond
	t.Cleanup(func() { comfyPollEvery = oldPoll })

	var polls int32
	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&polls, 1) < 3 {
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		comfyHistoryDone(w)
	})
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if polls != 3 {
		t.Errorf("polls = %d, want 3 (an empty history is a picture still being made)", polls)
	}
}

// Caps advertises all three ops since P2's remaining work (the per-family image-to-image path),
// and one reference image — the same shape sdcpp reports, because a second one would need a
// graph nobody has run.
func TestComfyCapsOffersImageToImage(t *testing.T) {
	p, _ := comfyStub(t, sdxlConn(), nil)
	caps := p.Caps("sdxl-base-1.0")
	for _, op := range []Op{OpGenerate, OpEdit, OpInpaint} {
		if !caps.Supports(op) {
			t.Errorf("ops = %v, want %s among them", caps.Ops, op)
		}
	}
	if caps.Supports(OpOutpaint) || caps.Supports(OpUpscale) {
		t.Errorf("ops = %v — comfy must not claim an op with no template", caps.Ops)
	}
	if caps.MaxInputs != 1 {
		t.Errorf("max inputs = %d, want 1", caps.MaxInputs)
	}
}

// --- ADR 0094: Qwen-Image-Edit-2509's family attributes and the union in Caps("") ------------

// qwenEditConn holds one ordinary guided checkpoint (sdxl, warm) alongside one row of the new
// edit-only family, so the two can be told apart by Caps(model) and compared against the union
// Caps("") answers with.
func qwenEditConn() EngineConn {
	c := sdxlConn()
	c.Warm = "sdxl-base-1.0"
	c.Models = append(c.Models, "qwen-edit-row")
	c.BaseModel["qwen-edit-row"] = "qwen-image-edit-2509"
	c.Files["qwen-edit-row"] = []EngineFile{
		{Flag: "--diffusion-model", Name: "qwen_image_edit_2509.safetensors"},
		{Flag: "--clip_l", Name: "qwen_2.5_vl_7b.safetensors"},
		{Flag: "--vae", Name: "qwen_image_vae.safetensors"},
	}
	return c
}

// Named, Caps is STRICT per family: the new family offers edit only, takes no strength and no
// size candidates — the opposite answer from the sdxl row on the same engine.
func TestComfyCapsQwenImageEditIsEditOnlyAndTakesNoStrengthOrSizes(t *testing.T) {
	p, _ := comfyStub(t, qwenEditConn(), nil)
	caps := p.Caps("qwen-edit-row")
	if caps.Supports(OpGenerate) || caps.Supports(OpInpaint) {
		t.Errorf("ops = %v, want edit only", caps.Ops)
	}
	if !caps.Supports(OpEdit) {
		t.Errorf("ops = %v, want edit among them", caps.Ops)
	}
	if caps.Strength {
		t.Error("qwen-image-edit-2509 must not advertise strength — its denoise is fixed at 1")
	}
	if len(caps.Sizes) != 0 {
		t.Errorf("sizes = %v, want none — the family decides the size from the input picture", caps.Sizes)
	}
	// 2 since P3 wired image2 (ADR 0094 decision 5). NOT 3: the node takes image3 and the wiring
	// is a loop, so the only thing keeping this honest is that three references have never been
	// run — the number and the measurement move together.
	if caps.MaxInputs != 2 {
		t.Errorf("max inputs = %d, want 2 (image2 is wired; image3 is unmeasured)", caps.MaxInputs)
	}
	// The positive control: the OTHER row on the same engine still answers the old way, so the
	// difference above is the family's and not some engine-wide change.
	sdxl := p.Caps("sdxl-base-1.0")
	if !sdxl.Supports(OpGenerate) || !sdxl.Strength || len(sdxl.Sizes) == 0 {
		t.Errorf("sdxl caps = %+v, want the ordinary shape unaffected", sdxl)
	}
	if sdxl.MaxInputs != 1 {
		t.Errorf("sdxl max inputs = %d, want 1 — only the instruction-edit families read a second reference", sdxl.MaxInputs)
	}
}

// 🔴 ADR 0094 decision 11: Caps("") is a UNION, not the warm row's own answer. With the edit-only
// family warm, the union still has to offer generate — otherwise HandleStatus stops advertising
// it and chooseImageProviders drops this engine from `op=generate`'s candidates, sending the next
// call to a provider that spends a member's own plan quota.
func TestComfyCapsEmptyModelIsAUnionAcrossEveryRow(t *testing.T) {
	conn := qwenEditConn()
	conn.Warm = "qwen-edit-row" // the edit-only family is what happens to be warm
	p, _ := comfyStub(t, conn, nil)
	union := p.Caps("")
	if !union.Supports(OpGenerate) {
		t.Errorf("ops = %v, want generate offered — sdxl on the same engine can do it", union.Ops)
	}
	if !union.Supports(OpEdit) {
		t.Errorf("ops = %v, want edit offered too", union.Ops)
	}
	if !union.Strength {
		t.Error("strength = false, want true — sdxl on the same engine takes it")
	}
	// The negative control for the union itself: an engine with ONLY the edit-only family must
	// not claim an op nothing on it can do.
	editOnly, _ := comfyStub(t, EngineConn{
		Models: []string{"qwen-edit-row"}, BaseModel: map[string]string{"qwen-edit-row": "qwen-image-edit-2509"},
		Files: map[string][]EngineFile{"qwen-edit-row": conn.Files["qwen-edit-row"]},
	}, nil)
	only := editOnly.Caps("")
	if only.Supports(OpGenerate) || only.Supports(OpInpaint) {
		t.Errorf("ops = %v, want edit only when that is the only family on the engine", only.Ops)
	}
	if only.Strength {
		t.Error("strength = true, want false — nothing on this engine takes it")
	}
}

// The op-resolution half of decision 11: a request naming no model lands on the warm row, and
// when that row's family cannot do the requested op, Generate looks for the first row (in
// catalogue order) that can — rather than fail the op and let Run() fall through to a provider
// that spends a member's own plan (ADR 0071 P1's measured accident, imagegen.go's
// fallbackWarnings).
func TestComfyGenerateRemapsToAnotherRowWhenTheWarmFamilyCannotDoTheOp(t *testing.T) {
	conn := qwenEditConn()
	conn.Warm = "qwen-edit-row"
	var gotModel string
	p, _ := comfyStub(t, conn, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotModel = r.Header.Get("X-AF-Model")
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if gotModel != "sdxl-base-1.0" {
		t.Errorf("model sent to the engine = %q, want the row that can generate", gotModel)
	}
	if res.Model != "sdxl-base-1.0" {
		t.Errorf("result model = %q, want sdxl-base-1.0", res.Model)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "switching") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a switch warning explaining the checkpoint change", res.Warnings)
	}

	// An EXPLICIT model is honoured and refused as asked, never remapped: the caller named it on
	// purpose (the same rule Run() itself follows for an explicit provider).
	_, err = p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "qwen-edit-row"})
	if err == nil || !strings.Contains(err.Error(), "cannot do") {
		t.Errorf("err = %v, want an explicit model's own refusal, not a silent remap", err)
	}
}

// 🔴 sfiowgj review (2026-09-20): a request naming a PROVIDER but no model, whose WARM row does
// not offer the requested op, must not be refused for size or strength on that warm row's own
// family — Generate() itself remaps AWAY from it (comfyFirstModelForOp, the test above), so
// refusing at the edge against the row that will never actually run the request would 400 the
// pane's own default case (no model chosen yet, plain generate) the instant an edit-only
// checkpoint happens to be warm. comfyResolveFamily has to skip the warm row for this exact
// reason when it is not an explicit choice.
func TestComfySizeAndStrengthRefusalDoNotFireWhenTheWarmRowWouldBeRemappedAway(t *testing.T) {
	conn := qwenEditConn()
	conn.Warm = "qwen-edit-row"
	p, _ := comfyStub(t, conn, nil)
	withStubProvider(t, p)

	if msg := comfySizeRefusal(p.ID(), "", "generate", "1024x1024"); msg != "" {
		t.Errorf("comfySizeRefusal = %q, want silence — a plain generate remaps away from the warm qwen row", msg)
	}
	if msg := comfyStrengthRefusal(p.ID(), "", "generate"); msg != "" {
		t.Errorf("comfyStrengthRefusal = %q, want silence for the same reason", msg)
	}

	// The positive control: the SAME warm row, asked for the op it itself will actually run
	// (edit), still refuses normally — this is not a blanket "provider named, no model" bypass.
	if msg := comfySizeRefusal(p.ID(), "", "edit", "1024x1024"); msg == "" {
		t.Error("comfySizeRefusal was silent for an op the warm row itself answers")
	}
	if msg := comfyStrengthRefusal(p.ID(), "", "edit"); msg == "" {
		t.Error("comfyStrengthRefusal was silent for an op the warm row itself answers")
	}
	// 🟡 sfiowgj review: an EXPLICIT model with an op it cannot do is ALSO silent here — the
	// real reason is decision 13's "cannot do %s", which Generate()/Run() refuse with; reporting
	// size/strength here first would name the smaller of two true reasons.
	if msg := comfySizeRefusal(p.ID(), "qwen-edit-row", "generate", "1024x1024"); msg != "" {
		t.Errorf("comfySizeRefusal = %q, want silence — the op mismatch is the real, bigger reason", msg)
	}
}

// 🟡 sfiowgj review: a row whose family offers the op but whose declared files are incomplete
// must be SKIPPED, not returned — otherwise Generate() would remap to it, fail building its
// graph, and Run() falls through to a provider that spends a member's own plan anyway, which is
// the exact outcome decision 11's remap exists to avoid.
func TestComfyFirstModelForOpSkipsARowWithIncompleteFiles(t *testing.T) {
	conn := qwenEditConn()
	// A row ahead of the working sdxl one that claims the sdxl family but declares no files at
	// all — comfyBuildGraph must refuse it, and comfyFirstModelForOp must move past it rather
	// than returning a row that cannot actually generate.
	conn.Models = append([]string{"broken-sdxl"}, conn.Models...)
	conn.BaseModel["broken-sdxl"] = "sdxl"
	got, ok := comfyFirstModelForOp(conn, OpGenerate)
	if !ok || got != "sdxl-base-1.0" {
		t.Errorf("comfyFirstModelForOp = (%q, %v), want the row AFTER the broken one", got, ok)
	}
}

// The three refusals that must land before anything is uploaded and before a GPU is woken.
func TestComfyGenerateRefusesMismatchedAttachments(t *testing.T) {
	for _, c := range []struct {
		name, want string
		req        Request
	}{
		{"edit with no input", "needs an input image",
			Request{Op: OpEdit, Prompt: "a fox"}},
		{"inpaint with no mask", "needs a mask image",
			Request{Op: OpInpaint, Prompt: "a fox", Inputs: []string{"/tmp/x.png"}}},
		{"two reference images", "at most 1 reference image",
			Request{Op: OpEdit, Prompt: "a fox", Inputs: []string{"/tmp/x.png", "/tmp/y.png"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				t.Error("the engine was called for a request that cannot be built")
			})
			_, err := p.Generate(context.Background(), c.req)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// The whole image-to-image round trip, and the part of it a graph fixture cannot show: the bytes
// go to ComfyUI's own input directory FIRST (LoadImage's `image` is an enumeration over that
// directory, not a path), and the graph names what came back.
func TestComfyEditUploadsTheInputAndNamesItInTheGraph(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(in, tinyPNG(t, 640, 480), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploadedField, uploadedType, gotCType string
	var uploads int32
	var gotGraph map[string]any
	p := comfyEditStub(t, sdxlConn(),
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&uploads, 1)
			gotCType = r.Header.Get("Content-Type")
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Errorf("the upload was not multipart: %v", err)
			}
			for name := range r.MultipartForm.File {
				uploadedField = name
			}
			uploadedType = r.FormValue("type")
			// The engine renames a colliding upload, so the graph must use THIS name and not
			// the one that was sent.
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "renamed-by-engine.png", "subfolder": "", "type": "input"})
		},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			gotGraph, _ = body["prompt"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
		})

	res, err := p.Generate(context.Background(), Request{
		Op: OpEdit, Prompt: "make it snow", Inputs: []string{in}, Size: "1024x1024",
	})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if uploads != 1 || uploadedField != "image" || uploadedType != "input" {
		t.Errorf("uploads = %d, field = %q, type = %q", uploads, uploadedField, uploadedType)
	}
	if !strings.HasPrefix(gotCType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart (the gateway forwards it verbatim)", gotCType)
	}
	img, ok := gotGraph["img"].(map[string]any)
	if !ok {
		t.Fatalf("no LoadImage node in the graph: %v", gotGraph)
	}
	inputs, _ := img["inputs"].(map[string]any)
	if img["class_type"] != "LoadImage" || inputs["image"] != "renamed-by-engine.png" {
		t.Errorf("LoadImage = %v, want it to name what /upload/image answered", img)
	}
	// The sampler has to start from the encoded picture, not from an empty latent.
	if _, empty := gotGraph["lat"]; empty {
		t.Error("an empty latent was built for an edit")
	}
	ks, _ := gotGraph["ks"].(map[string]any)
	ksIn, _ := ks["inputs"].(map[string]any)
	if link, _ := ksIn["latent_image"].([]any); len(link) != 2 || link[0] != "enc" {
		t.Errorf("latent_image = %v, want the VAEEncode output", ksIn["latent_image"])
	}
	if ksIn["denoise"] != comfyEditDenoise {
		t.Errorf("denoise = %v, want the edit recipe's %v", ksIn["denoise"], comfyEditDenoise)
	}
	// The size an edit produces is the input picture's, so a request that asked for another one
	// is told rather than left to notice.
	found := false
	for _, warning := range res.Warnings {
		if strings.Contains(warning, "640x480") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want the input picture's own size named", res.Warnings)
	}
}

// inpaint adds the mask, and the mask is read off the RED channel: LoadImage's own MASK output is
// 1-alpha, so an opaque black-and-white PNG through the alpha path would repaint nothing at all
// and say nothing about it.
func TestComfyInpaintUploadsTheMaskAndSetsTheNoiseMask(t *testing.T) {
	dir := t.TempDir()
	in, mask := filepath.Join(dir, "photo.png"), filepath.Join(dir, "mask.png")
	if err := os.WriteFile(in, tinyPNG(t, 64, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mask, tinyPNG(t, 64, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploads int32
	var gotGraph map[string]any
	p := comfyEditStub(t, sdxlConn(),
		func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&uploads, 1)
			_ = r.ParseMultipartForm(8 << 20)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("up-%d.png", n), "type": "input"})
		},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			gotGraph, _ = body["prompt"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
		})

	if _, err := p.Generate(context.Background(), Request{
		Op: OpInpaint, Prompt: "a hat", Inputs: []string{in}, Mask: mask,
	}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if uploads != 2 {
		t.Fatalf("uploads = %d, want the picture and the mask", uploads)
	}
	maskNode, ok := gotGraph["mask"].(map[string]any)
	if !ok {
		t.Fatalf("no mask node in the graph: %v", gotGraph)
	}
	maskIn, _ := maskNode["inputs"].(map[string]any)
	if maskNode["class_type"] != "LoadImageMask" || maskIn["channel"] != "red" {
		t.Errorf("mask node = %v, want LoadImageMask on the red channel", maskNode)
	}
	if maskIn["image"] != "up-2.png" {
		t.Errorf("the mask node names %v, want the SECOND upload (the first is the picture)", maskIn["image"])
	}
	ks, _ := gotGraph["ks"].(map[string]any)
	ksIn, _ := ks["inputs"].(map[string]any)
	if link, _ := ksIn["latent_image"].([]any); len(link) != 2 || link[0] != "noisemask" {
		t.Errorf("latent_image = %v, want the masked latent", ksIn["latent_image"])
	}
	// Inpaint stays at full denoise: the mask preserves what is outside it, not a partial one.
	if ksIn["denoise"] != float64(1) {
		t.Errorf("denoise = %v, want 1 for inpaint", ksIn["denoise"])
	}
}

// A picture larger than what the engine gateway will buffer is refused BY NAME here. The gateway
// truncates at 32 MiB rather than failing, so without this the file would arrive corrupt and come
// back as a decoder error naming nothing the caller can act on.
func TestComfyRefusesAnOversizedUpload(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, make([]byte, comfyMaxUpload+1), 0o600); err != nil {
		t.Fatal(err)
	}
	p := comfyEditStub(t, sdxlConn(),
		func(w http.ResponseWriter, r *http.Request) { t.Error("an oversized picture reached the engine") },
		nil)
	_, err := p.Generate(context.Background(), Request{Op: OpEdit, Prompt: "x", Inputs: []string{big}})
	if err == nil || !strings.Contains(err.Error(), "limit for one picture") {
		t.Errorf("err = %v, want a refusal naming the limit", err)
	}
}

// A model with no declared baseModel is refused rather than guessed at — decision 2 exists so
// the family is a stated fact, and comfy has no id-sniffing fallback at all.
//
// The refusal has to separate the two ways it happens, because they need different fixes and
// only one of them LOOKS wrong on the admin screen: nothing declared (a seeded row) versus an
// upstream display name that names no template ("SDXL 1.0" — what Civitai publishes, and what
// the ingest path stored until ADR 0072 P2's 実機検証). Either way it names the vocabulary,
// because "declare a family" is useless without the five spellings.
func TestComfyGenerateRefusesAModelWithNoDeclaredFamily(t *testing.T) {
	for _, c := range []struct{ name, declared, want string }{
		{"undeclared", "", "declares no checkpoint family"},
		{"an upstream display name", "SDXL 1.0", `declares the checkpoint family "SDXL 1.0"`},
		{"a plausible id that names no template", "sdxl-turbo", `"sdxl-turbo"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn := EngineConn{Models: []string{"mystery-model"}}
			if c.declared != "" {
				conn.BaseModel = map[string]string{"mystery-model": c.declared}
			}
			p, _ := comfyStub(t, conn, nil)
			_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "mystery-model"})
			if err == nil {
				t.Fatal("no error — the graph was built from a family nothing declared")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
			// Without the list, an operator is told to declare something and not what.
			for _, fam := range []string{"sdxl", "flux2-klein", "zimage"} {
				if !strings.Contains(err.Error(), fam) {
					t.Errorf("err = %v, does not name %q as a choice", err, fam)
				}
			}
		})
	}
}

// --- LoRAs (ADR 0072 decision 5, phase P3) --------------------------------------------------

// loraConn is an engine holding two checkpoints of different families and one LoRA for each, so
// every pairing — right and wrong — can be asked for.
func loraConn() EngineConn {
	c := sdxlConn()
	c.Models = []string{"sdxl-base-1.0", "klein-4b"}
	c.BaseModel["klein-4b"] = "flux2-klein"
	c.Loras = []EngineLora{
		{ID: "watercolor-v2", File: "watercolor-v2.safetensors", BaseModel: "sdxl", Description: "soft watercolour"},
		{ID: "klein-lineart", File: "klein-lineart.safetensors", BaseModel: "flux2-klein"},
	}
	return c
}

// The whole enum, not the subset that fits the model, even though Caps is per (provider, model):
// the tool schema is built once per tools/list, before any checkpoint is chosen, so an enum that
// depended on `model` would be a promise this layer cannot keep (ADR 0072 決定 5 の改訂).
func TestComfyCapsListsEveryLoraRegardlessOfModel(t *testing.T) {
	p, _ := comfyStub(t, loraConn(), nil)
	for _, model := range []string{"sdxl-base-1.0", "klein-4b", ""} {
		got := p.Caps(model).Loras
		if len(got) != 2 {
			t.Fatalf("Caps(%q).Loras = %v, want both LoRAs", model, got)
		}
		if got[0].BaseModel != "sdxl" || got[1].BaseModel != "flux2-klein" {
			t.Errorf("Caps(%q).Loras = %+v, want each entry to carry its own family (the caller pairs them)", model, got)
		}
		if got[0].Description == "" {
			t.Errorf("Caps(%q).Loras[0] lost the catalogue's description", model)
		}
	}
}

// The refusal ADR 0072 レビュー決定 5 moved out of the Control Plane and into the Agent: a LoRA
// trained for another family does not fail on the engine, it quietly does nothing to the picture.
func TestComfyGenerateRefusesALoraFromAnotherFamily(t *testing.T) {
	p, _ := comfyStub(t, loraConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Error("the engine was called for a pairing that cannot work")
	})
	_, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0",
		Loras: []LoraRef{{Name: "klein-lineart"}},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"klein-lineart", "flux2-klein", "sdxl-base-1.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

// A LoRA row nobody finished declaring is refused too, and separately: pairing it with anything
// would be a guess, which is the same reason a checkpoint with no declared family is refused.
func TestComfyGenerateRefusesALoraWithNoDeclaredFamily(t *testing.T) {
	conn := loraConn()
	conn.Loras = append(conn.Loras, EngineLora{ID: "mystery", File: "mystery.safetensors"})
	p, _ := comfyStub(t, conn, nil)
	_, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0", Loras: []LoraRef{{Name: "mystery"}},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "declares no checkpoint family") || !strings.Contains(err.Error(), "base_model") {
		t.Errorf("err = %v, want it to say what is missing and where to set it", err)
	}
}

// An unknown name says what IS enabled, with each one's family — without that, finding out costs
// the caller another turn.
func TestComfyGenerateRefusesAnUnknownLoraAndNamesTheAlternatives(t *testing.T) {
	p, _ := comfyStub(t, loraConn(), nil)
	_, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0", Loras: []LoraRef{{Name: "made-up"}},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"made-up", "watercolor-v2 (sdxl)", "klein-lineart (flux2-klein)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

// A matching pairing reaches the graph: the node carries the on-disk basename, not the catalogue
// id, because lora_name is an enumeration over what the box actually holds.
func TestComfyGenerateSendsAMatchingLoraToTheEngine(t *testing.T) {
	var gotGraph map[string]any
	p, _ := comfyStub(t, loraConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotGraph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	if _, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0",
		Loras: []LoraRef{{Name: "watercolor-v2", Weight: 0.6}},
	}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	node, ok := gotGraph["lora1"].(map[string]any)
	if !ok {
		t.Fatalf("no lora1 node in the graph sent to /prompt: %v", gotGraph)
	}
	inputs, _ := node["inputs"].(map[string]any)
	if inputs["lora_name"] != "watercolor-v2.safetensors" {
		t.Errorf("lora_name = %v, want the file name the box holds", inputs["lora_name"])
	}
	if inputs["strength_model"] != 0.6 {
		t.Errorf("strength_model = %v, want the requested weight", inputs["strength_model"])
	}
}

// ADR 0081 decision 5. The pairing is legal, the adapter loads, the whole generation is paid for
// — and the picture is the one without it, because the words it answers to were never said. There
// is no error to observe, so the result has to say it out loud.
func TestComfyGenerateWarnsWhenATriggerWordIsMissing(t *testing.T) {
	conn := loraConn()
	conn.Loras[0].TrainedWords = []string{"wtrcolor style", "loose wash"}
	p, _ := comfyStub(t, conn, nil)

	res, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0",
		Loras: []LoraRef{{Name: "watercolor-v2"}},
	})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "watercolor-v2") && strings.Contains(w, "wtrcolor style") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the adapter and the words it answers to", res.Warnings)
	}

	// And silent when the prompt says one of them: a warning that fires on the correct request
	// is a warning nobody reads on the incorrect one.
	ok, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox, LOOSE WASH", Model: "sdxl-base-1.0",
		Loras: []LoraRef{{Name: "watercolor-v2"}},
	})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	for _, w := range ok.Warnings {
		if strings.Contains(w, "answers to") {
			t.Errorf("warnings = %v, want nothing once a trigger word is in the prompt", ok.Warnings)
		}
	}
}

// The edges of the same rule, where a wrong answer is a warning that cries wolf (and gets ignored
// on the request that mattered) rather than a broken picture.
func TestComfyTriggerWarnings(t *testing.T) {
	conn := loraConn()
	conn.Loras[0].TrainedWords = []string{"wtrcolor style", "loose wash"}

	for _, tc := range []struct {
		name   string
		want   []LoraRef
		prompt string
		warn   bool
	}{
		{name: "no LoRA at all", prompt: "a fox"},
		{name: "none of the words", want: []LoraRef{{Name: "watercolor-v2"}}, prompt: "a fox", warn: true},
		{name: "ANY of them is enough — they are alternatives, not a checklist",
			want: []LoraRef{{Name: "watercolor-v2"}}, prompt: "a fox, loose wash"},
		{name: "the prompt's own casing does not decide it",
			want: []LoraRef{{Name: "watercolor-v2"}}, prompt: "A Fox, Wtrcolor Style"},
		{name: "an adapter that publishes none is not nagged about words it does not have",
			want: []LoraRef{{Name: "klein-lineart"}}, prompt: "a fox"},
		{name: "a name this engine does not have is comfyResolveLoras' refusal, not a warning",
			want: []LoraRef{{Name: "nothing-of-the-sort"}}, prompt: "a fox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := comfyTriggerWarnings(conn, tc.want, tc.prompt)
			if (len(got) > 0) != tc.warn {
				t.Fatalf("warnings = %v, want any = %v", got, tc.warn)
			}
		})
	}
}

// The rest of comfyResolveLoras' contract, in one table: an unstated weight is decision 5's
// default of 1, the range is 0-2, and neither a repeat nor an unbounded pile is accepted.
func TestComfyResolveLoras(t *testing.T) {
	conn := loraConn()
	t.Run("an unstated weight is 1", func(t *testing.T) {
		got, err := comfyResolveLoras(conn, ComfyFamilySDXL, "sdxl-base-1.0", []LoraRef{{Name: "watercolor-v2"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Weight != 1 {
			t.Errorf("resolved %+v, want the default strength of 1", got)
		}
	})
	t.Run("out of range is refused", func(t *testing.T) {
		for _, w := range []float64{-1, 2.5} {
			_, err := comfyResolveLoras(conn, ComfyFamilySDXL, "sdxl-base-1.0", []LoraRef{{Name: "watercolor-v2", Weight: w}})
			if err == nil {
				t.Errorf("strength %g was accepted, want the 0-2 range enforced", w)
			}
		}
	})
	t.Run("the same LoRA twice is refused", func(t *testing.T) {
		_, err := comfyResolveLoras(conn, ComfyFamilySDXL, "sdxl-base-1.0",
			[]LoraRef{{Name: "watercolor-v2"}, {Name: "watercolor-v2", Weight: 0.5}})
		if err == nil {
			t.Error("a repeated LoRA was accepted")
		}
	})
	t.Run("more than the cap is refused", func(t *testing.T) {
		want := make([]LoraRef, comfyMaxLoras+1)
		for i := range want {
			want[i] = LoraRef{Name: "watercolor-v2"}
		}
		if _, err := comfyResolveLoras(conn, ComfyFamilySDXL, "sdxl-base-1.0", want); err == nil {
			t.Error("an unbounded chain was accepted")
		}
	})
	t.Run("an engine with none says so", func(t *testing.T) {
		_, err := comfyResolveLoras(sdxlConn(), ComfyFamilySDXL, "sdxl-base-1.0", []LoraRef{{Name: "watercolor-v2"}})
		if err == nil || !strings.Contains(err.Error(), "no LoRA is enabled") {
			t.Errorf("err = %v, want it to say the catalogue enables none", err)
		}
	})
	t.Run("asking for none stays empty", func(t *testing.T) {
		got, err := comfyResolveLoras(conn, ComfyFamilySDXL, "sdxl-base-1.0", nil)
		if err != nil || got != nil {
			t.Errorf("resolved %+v, %v — want nothing at all", got, err)
		}
	})
}

// Ready is "we hold a token for this engine", never "the engine is up" — the same rule sdcpp
// follows, for the same reason: an asleep engine is normal and must not drop the tool from
// tools/list.
func TestComfyReadyIsTokenOnly(t *testing.T) {
	if (&comfyProvider{}).Ready(context.Background()) {
		t.Error("nil lookup must not be ready")
	}
	none := &comfyProvider{lookup: func(context.Context) (EngineConn, bool) { return EngineConn{}, false }}
	if none.Ready(context.Background()) {
		t.Error("lookup returning false must not be ready")
	}
	noToken := &comfyProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://x"}, true
	}}
	if noToken.Ready(context.Background()) {
		t.Error("an empty token must not be ready")
	}
	full := &comfyProvider{lookup: func(context.Context) (EngineConn, bool) {
		return EngineConn{BaseURL: "http://x", Token: "t"}, true
	}}
	if !full.Ready(context.Background()) {
		t.Error("base url + token must be ready")
	}
}

// --- the pinned seed (ADR 0069 follow-up) ---------------------------------------------------

// comfySeedIn reads the seed out of whichever node the family's sampler keeps it in.
func comfySeedIn(t *testing.T, graph map[string]any, node, field string) any {
	t.Helper()
	n, ok := graph[node].(map[string]any)
	if !ok {
		t.Fatalf("the graph has no %s node: %v", node, graph)
	}
	in, _ := n["inputs"].(map[string]any)
	return in[field]
}

// A pinned seed reaches the sampler verbatim — which is the whole point: two requests differing
// in one thing can only be compared when everything else, the noise included, is identical.
func TestComfyGeneratePinsTheRequestedSeed(t *testing.T) {
	var gotGraph map[string]any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotGraph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	seed := int64(1234)
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Seed: &seed}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if got := comfySeedIn(t, gotGraph, "ks", "seed"); got != float64(1234) {
		t.Errorf("KSampler seed = %v, want the pinned 1234", got)
	}
}

// Seed 0 is a seed, not "unset". A provider that read the zero value as absent would hand back a
// random picture to the one caller who was most explicit about what they wanted.
func TestComfyGeneratePinsSeedZero(t *testing.T) {
	var gotGraph map[string]any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotGraph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	seed := int64(0)
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Seed: &seed}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if got := comfySeedIn(t, gotGraph, "ks", "seed"); got != float64(0) {
		t.Errorf("KSampler seed = %v, want the pinned 0", got)
	}
}

// The positive control for both tests above: with no seed pinned, two requests must differ. A
// provider that quietly reused one value would make the tests above pass for the wrong reason.
func TestComfyGenerateWithoutASeedIsRandomEachTime(t *testing.T) {
	var seeds []any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		g, _ := body["prompt"].(map[string]any)
		seeds = append(seeds, comfySeedIn(t, g, "ks", "seed"))
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	for i := 0; i < 2; i++ {
		if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"}); err != nil {
			t.Fatalf("Generate() = %v", err)
		}
	}
	if len(seeds) != 2 || seeds[0] == seeds[1] {
		t.Errorf("seeds = %v, want two different random ones", seeds)
	}
}

// comfy is the only route that takes a seed, because it is the only one whose request body this
// package writes in full.
func TestComfyCapsTakesASeed(t *testing.T) {
	p, _ := comfyStub(t, sdxlConn(), nil)
	if !p.Caps("sdxl-base-1.0").Seed {
		t.Error("comfy must advertise that it takes a pinned seed")
	}
}

// A picture the engine did not generate is reported as such, and only when the engine itself
// said so: ComfyUI's `execution_cached` names the nodes it skipped, and the SaveImage node being
// among them means nothing was made. A caller who pinned a seed on purpose wants this; a caller
// who changed something the graph does not carry needs it.
func TestComfyGenerateWarnsWhenTheEngineServedFromCache(t *testing.T) {
	const promptID = "af-test-prompt"
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
			"status": map[string]any{"completed": true, "status_str": "success",
				"messages": []any{
					[]any{"execution_start", map[string]any{"prompt_id": promptID}},
					[]any{"execution_cached", map[string]any{"nodes": []any{"ckpt", "pos", "ks", "save"}}},
				}},
			"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{
				{"filename": "af-sdxl_00001_.png"}}}},
		}})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tinyPNG(t, 1, 1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &comfyProvider{client: srv.Client(), lookup: func(context.Context) (EngineConn, bool) {
		c := sdxlConn()
		c.BaseURL, c.Token = srv.URL+"/engine/image/v1", "afe_test"
		return c, true
	}}

	seed := int64(7)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Seed: &seed})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "cache") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the engine's cache", res.Warnings)
	}
}

// The negative control: a normal run caches nothing, and must stay silent. Without this the
// warning could be unconditional and the test above would still pass.
func TestComfyGenerateSaysNothingAboutCacheOnANormalRun(t *testing.T) {
	p, _ := comfyStub(t, sdxlConn(), nil)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "cache") {
			t.Errorf("unexpected cache warning on a fresh generation: %v", res.Warnings)
		}
	}
}

// A cache hit that skipped SOME nodes but still ran the output node is a normal partial reuse
// (the checkpoint loader, the text encode) and is not worth a word: a picture was made.
func TestComfyCacheWarningIgnoresAPartialReuse(t *testing.T) {
	var hist comfyHistory
	hist.Status.Messages = []any{
		[]any{"execution_cached", map[string]any{"nodes": []any{"ckpt", "pos", "neg"}}},
	}
	if got := comfyCacheWarning(hist); got != "" {
		t.Errorf("comfyCacheWarning = %q, want silence when the output node still ran", got)
	}
}

// --- negative prompts (ADR 0072 follow-up) ---------------------------------------------------

// negConn is an engine holding one guided checkpoint that declares its own negative prompt and
// one distilled checkpoint that cannot take one, with the deployment's own exclusion list set.
func negConn() EngineConn {
	c := sdxlConn()
	c.Models = []string{"sdxl-base-1.0", "klein-4b"}
	c.BaseModel["klein-4b"] = "flux2-klein"
	c.Files["klein-4b"] = []EngineFile{
		{Flag: "--diffusion-model", Name: "klein.safetensors"},
		{Flag: "--clip_l", Name: "qwen.safetensors"},
		{Flag: "--vae", Name: "flux2-vae.safetensors"},
	}
	// Both Krea 2 modes of ONE guided family: the Turbo row takes the family's own recipe (cfg 1)
	// and the Raw row declares the undistilled numbers. They exist to be compared — see
	// TestComfyCapsNegativeReadsTheRowsCfg.
	for _, id := range []string{"krea2-turbo", "krea2-raw"} {
		c.Models = append(c.Models, id)
		c.BaseModel[id] = "krea2"
		c.Files[id] = []EngineFile{
			{Flag: "--diffusion-model", Name: "krea2.safetensors"},
			{Flag: "--clip_l", Name: "qwen3vl_4b.safetensors"},
			{Flag: "--vae", Name: "qwen_image_vae.safetensors"},
		}
	}
	c.Params = map[string]EngineParams{"krea2-raw": {Steps: 52, CFG: 4.5}}
	c.Negatives = map[string]string{"sdxl-base-1.0": "extra fingers"}
	c.NegativeAlways = "explicit"
	return c
}

// comfyPromptText reads a CLIPTextEncode's text off the graph the stub was sent, which is the
// only place the composed negative can be observed: nothing in the result says what was excluded.
func comfyPromptText(t *testing.T, graph map[string]any, node string) string {
	t.Helper()
	n, ok := graph[node].(map[string]any)
	if !ok {
		t.Fatalf("the graph has no node %q", node)
	}
	inputs, _ := n["inputs"].(map[string]any)
	s, _ := inputs["text"].(string)
	return s
}

// The three declaring places are ADDED, in one order, and none of them can drop another: the
// catalogue row's default, the caller's own, then the deployment's exclusion list. A caller
// naming one thing to keep out does not mean "and stop excluding what the publisher recommends",
// and the administrator's list is the part no request may drop at all.
func TestComfyNegativeAddsTheRowTheCallerAndTheDeployment(t *testing.T) {
	var graph map[string]any
	p, _ := comfyStub(t, negConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		graph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	if _, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0", NegativePrompt: "watermark"}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if got, want := comfyPromptText(t, graph, "neg"), "extra fingers, watermark, explicit"; got != want {
		t.Errorf("negative = %q, want %q", got, want)
	}
	// The positive prompt must not have collected any of it — the whole point of the separate
	// axis is that these words are conditioned AGAINST, not FOR.
	if got := comfyPromptText(t, graph, "pos"); got != "a fox" {
		t.Errorf("positive = %q, want the caller's prompt alone", got)
	}
}

// Nobody declaring anything leaves the graph exactly as it has always been, which is what keeps
// the golden fixtures meaningful: the fixed default is a fallback, not a floor the three sources
// are appended to.
func TestComfyNegativeFallsBackToTheFixedDefault(t *testing.T) {
	var graph map[string]any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		graph, _ = body["prompt"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "af-test-prompt"})
	})
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"}); err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if got := comfyPromptText(t, graph, "neg"); got != comfyNegativePrompt {
		t.Errorf("negative = %q, want the measured default %q", got, comfyNegativePrompt)
	}
}

// Caps answers per MODEL, not per provider: on one running engine an SDXL checkpoint takes a
// negative prompt and a distilled one cannot. Reporting the provider's answer for both would
// either hide the argument from a session that can use it or promise one that does nothing.
func TestComfyCapsNegativeIsPerModel(t *testing.T) {
	p, _ := comfyStub(t, negConn(), nil)
	if !p.Caps("sdxl-base-1.0").Negative {
		t.Error("sdxl reports no negative prompt, and its KSampler runs at cfg 7")
	}
	if p.Caps("klein-4b").Negative {
		t.Error("flux2-klein reports a negative prompt — it samples at cfg 1, where the branch cancels out")
	}
	// An undeclared family is refused before a graph exists, so "it would have been honoured" is
	// not a thing to have said.
	if p.Caps("nothing-declared").Negative {
		t.Error("a model with no declared family reports a negative prompt")
	}
}

// The family is necessary and not sufficient: two rows of the SAME guided family answer
// differently because one of them is declared at cfg 1, where `uncond + 1*(cond - uncond)` is
// cond exactly and the branch the template wires cancels out. Krea 2 Turbo is that row, and it
// is the normal one for the family — so answering by family alone would tell most Krea 2 users
// their negative prompt reaches a picture it cannot touch.
func TestComfyCapsNegativeReadsTheRowsCfg(t *testing.T) {
	p, _ := comfyStub(t, negConn(), nil)
	if p.Caps("krea2-turbo").Negative {
		t.Error("a krea2 row at the family's cfg 1 reports a negative prompt that cancels out")
	}
	// The positive control, and the reason this is not just "krea2 is distilled": the same
	// family, the same template, one declared `params` apart.
	if !p.Caps("krea2-raw").Negative {
		t.Error("a krea2 row declared at cfg 4.5 reports no negative prompt, and its KSampler runs guided")
	}
	// The form's fields have to say the same thing as the capability, or the member is offered a
	// box for a value the engine has already decided to ignore.
	if slices.Contains(comfyModelKnobs(negConn(), ComfyFamilyKrea2, "krea2-turbo"), "negative") {
		t.Error("the form offers a negative field on a row whose Caps say it is ignored")
	}
	if !slices.Contains(comfyModelKnobs(negConn(), ComfyFamilyKrea2, "krea2-raw"), "negative") {
		t.Error("the form drops the negative field on a guided row that reads it")
	}
}

// The administrator's exclusion list silently not applying is the failure this path exists to
// prevent, so a family that cannot take one says so in the result — even though the caller asked
// for nothing and nothing went wrong.
func TestComfyWarnsWhenTheFamilyCannotExclude(t *testing.T) {
	p, _ := comfyStub(t, negConn(), nil)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "klein-4b"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	var found string
	for _, w := range res.Warnings {
		if strings.Contains(w, "excludes") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("warnings = %v, want one saying the deployment's exclusions did not apply", res.Warnings)
	}
	if !strings.Contains(found, "flux2-klein") {
		t.Errorf("warning = %q, want it to name the family that cannot exclude", found)
	}
	// The negative control: the same engine and the same lists, on a family that CAN exclude,
	// must stay silent — a warning on every request is one nobody reads.
	ok, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "sdxl-base-1.0"})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	for _, w := range ok.Warnings {
		if strings.Contains(w, "excludes") {
			t.Errorf("warnings = %v, want nothing about exclusions on a guided family", ok.Warnings)
		}
	}
}

// --- cancel (ADR 0081 decision 2) -------------------------------------------------------------

// comfyCancelStub answers GET /queue with the given pending ids and records what POST /queue and
// POST /interrupt were sent.
func comfyCancelStub(t *testing.T, pending []string) (*comfyProvider, *[]string) {
	t.Helper()
	var sent []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /engine/image/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		entries := make([][]any, 0, len(pending))
		for _, id := range pending {
			entries = append(entries, []any{1, id, map[string]any{}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"queue_running": []any{}, "queue_pending": entries})
	})
	record := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		sent = append(sent, r.URL.Path+" "+string(b))
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}
	mux.HandleFunc("POST /engine/image/v1/queue", record)
	mux.HandleFunc("POST /engine/image/v1/interrupt", record)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	conn := EngineConn{BaseURL: srv.URL + "/engine/image/v1", Token: "afe_test"}
	return &comfyProvider{client: srv.Client(), lookup: func(context.Context) (EngineConn, bool) {
		return conn, true
	}}, &sent
}

// A prompt the engine has not started yet is DELETED from the queue: dropping it costs no
// sampling at all, and unlike an interrupt it cannot be confused with the job that is executing.
func TestComfyCancelDeletesAPromptThatIsStillPending(t *testing.T) {
	p, sent := comfyCancelStub(t, []string{"af-1", "af-2"})
	if err := p.Cancel(context.Background(), "af-2"); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "/queue") || !strings.Contains((*sent)[0], `"delete":["af-2"]`) {
		t.Fatalf("sent %v, want one /queue delete naming the prompt", *sent)
	}
}

// 🔴 A prompt that IS executing is interrupted BY ID. The bare /interrupt — the same route with
// no body — interrupts whatever the box happens to be running, and the box is shared across every
// workspace of the deployment.
func TestComfyCancelInterruptsWithThePromptId(t *testing.T) {
	p, sent := comfyCancelStub(t, []string{"someone-elses"})
	if err := p.Cancel(context.Background(), "af-mine"); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "/interrupt") {
		t.Fatalf("sent %v, want one /interrupt", *sent)
	}
	if !strings.Contains((*sent)[0], `"prompt_id":"af-mine"`) {
		t.Fatalf("sent %q with no prompt_id: a bare interrupt kills another workspace's picture", (*sent)[0])
	}
}

// An id this provider never issued is refused rather than turned into a bare interrupt.
func TestComfyCancelRefusesAnEmptyId(t *testing.T) {
	p, sent := comfyCancelStub(t, nil)
	if err := p.Cancel(context.Background(), "  "); err == nil {
		t.Fatal("an empty upstream id was accepted")
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %v for a request the engine never received", *sent)
	}
}

// Every picture of a batch carries its own seed: ComfyUI derives a batch's noise as base+i, so
// the second one is reproducible under base+1 and under nothing else.
func TestComfyStampsEachBatchPictureWithItsOwnSeed(t *testing.T) {
	const promptID = "af-batch"
	png := tinyPNG(t, 2, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
			"status": map[string]any{"completed": true, "status_str": "success"},
			"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{
				{"filename": "af-sdxl_00001_.png", "type": "output"},
				{"filename": "af-sdxl_00002_.png", "type": "output"},
			}}},
		}})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	conn := sdxlConn()
	conn.BaseURL, conn.Token = srv.URL+"/engine/image/v1", "afe_test"
	p := &comfyProvider{client: srv.Client(), lookup: func(context.Context) (EngineConn, bool) {
		return conn, true
	}}

	seed := int64(42)
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Count: 2, Seed: &seed})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if len(res.Images) != 2 {
		t.Fatalf("images = %d", len(res.Images))
	}
	for i, want := range []int64{42, 43} {
		if res.Images[i].Seed == nil || *res.Images[i].Seed != want {
			t.Errorf("image %d seed = %v, want %d", i, res.Images[i].Seed, want)
		}
	}
}

// The phases are what tell the person "the engine is starting" instead of an unexplained
// five-minute running.
func TestComfyReportsThePhasesAndTheEngineId(t *testing.T) {
	const promptID = "af-phases"
	var attempts int
	png := tinyPNG(t, 2, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "engine_waking", "message": "starting the box; retry"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
			"status": map[string]any{"completed": true, "status_str": "success"},
			"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{
				{"filename": "af-sdxl_00001_.png", "type": "output"}}}},
		}})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(png)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	conn := sdxlConn()
	conn.BaseURL, conn.Token = srv.URL+"/engine/image/v1", "afe_test"
	p := &comfyProvider{client: srv.Client(), lookup: func(context.Context) (EngineConn, bool) {
		return conn, true
	}}

	var phases []Phase
	var upstream string
	_, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: "a fox",
		OnPhase:    func(ph Phase) { phases = append(phases, ph) },
		OnUpstream: func(id string) { upstream = id },
	})
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if upstream != promptID {
		t.Errorf("upstream = %q, want the engine's own id — a cancel has nothing to aim at without it", upstream)
	}
	got := make([]string, 0, len(phases))
	for _, ph := range phases {
		got = append(got, string(ph))
	}
	if len(phases) < 3 || phases[0] != PhaseWaking || phases[len(phases)-1] != PhaseFetching {
		t.Fatalf("phases = %v, want waking first and fetching last", got)
	}
}

// --- per-family default sizes ---------------------------------------------------------------

// 🔴 SD1.5's UNet was trained at 512, and a 1024 request to it does not fail — it returns a
// picture with the subject duplicated. So the size a row falls back to has to be the FAMILY's,
// not one list shared by everything, and the row's own declaration still has to win over both.
//
// The megapixel families are pinned here too, byte-for-byte as they were before sizes became a
// per-family answer: this change must be invisible to them.
func TestComfySizesFallBackPerFamily(t *testing.T) {
	conn := EngineConn{
		Models: []string{"sd15-row", "sdxl-row", "declared-row", "undeclared-row"},
		BaseModel: map[string]string{
			"sd15-row": "sd15", "sdxl-row": "sdxl", "declared-row": "sd15",
		},
		Sizes: map[string][]string{"declared-row": {"1024x1024"}},
	}

	got := comfySizesFor(conn, "sd15-row")
	want := []string{"512x512", "512x768", "768x512", "640x512", "512x640"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sd15 sizes = %v, want %v", got, want)
	}
	for _, s := range got {
		if strings.HasPrefix(s, "1024") || strings.HasSuffix(s, "x1024") {
			t.Errorf("sd15 is offered %s, which is the size that duplicates the subject", s)
		}
	}

	if got := strings.Join(comfySizesFor(conn, "sdxl-row"), ","); got != strings.Join(comfyMegapixelSizes, ",") {
		t.Errorf("sdxl sizes = %v — the megapixel families must not move", got)
	}

	// The catalogue row still wins: an operator who declares 1024 for an SD1.5 row has said
	// something this table is not entitled to overrule.
	if got := comfySizesFor(conn, "declared-row"); len(got) != 1 || got[0] != "1024x1024" {
		t.Errorf("declared sizes = %v, want the row's own", got)
	}

	// An undeclared family cannot generate at all, so the list decides nothing — and answering
	// with SD1.5's presets there would be a guess about a row nobody has declared.
	if got := strings.Join(comfySizesFor(conn, "undeclared-row"), ","); got != strings.Join(comfyMegapixelSizes, ",") {
		t.Errorf("undeclared sizes = %v, want the megapixel list", got)
	}
}

// comfyDefaultSize is what a request naming no size gets, and it is the first preset of the
// family's own list — the native square, never another family's.
func TestComfyDefaultSizeIsTheFamilysNativeSquare(t *testing.T) {
	if w, h := comfyDefaultSize(ComfyFamilySD15); w != 512 || h != 512 {
		t.Errorf("sd15 default = %dx%d, want 512x512", w, h)
	}
	for _, f := range comfyFamilies {
		if f == ComfyFamilySD15 {
			continue
		}
		if w, h := comfyDefaultSize(f); w != 1024 || h != 1024 {
			t.Errorf("%s default = %dx%d, want the unchanged 1024x1024", f, w, h)
		}
	}
}

// Every family has to have a trial step count: the map is read with a plain lookup, so a family
// missing from it trials at 0 steps — which is not an error anywhere, just a blank picture.
func TestEveryFamilyHasTrialSteps(t *testing.T) {
	for _, f := range comfyFamilies {
		if comfyTrialSteps[f] <= 0 {
			t.Errorf("%s has no trial step count", f)
		}
	}
}

// --- the two clocks (ADR 0094, the P2 acceptance's by-product) ---------------------------------

// Waking the box and making the picture are on SEPARATE budgets, and this is the measured failure
// that made them separate: one clock covering both meant a cold start ate most of it and the
// sampling was cut off, so the caller got a failure while the GPU went on working and billing.
//
// The stub spends two thirds of the wake budget answering /prompt and then keeps the picture
// running well past the whole of it. With one clock this cannot pass; the assertion is simply
// that a picture comes back.
func TestComfyGenerationGetsItsOwnClockOnceTheEngineHasAccepted(t *testing.T) {
	const promptID = "af-test-prompt"
	restore := comfyShortClocks(t, 300*time.Millisecond, 5*time.Second)
	defer restore()

	start := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/engine/image/v1/prompt", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // two thirds of the wake budget, as a cold box would
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": promptID})
	})
	mux.HandleFunc("/engine/image/v1/history/"+promptID, func(w http.ResponseWriter, r *http.Request) {
		// Still sampling until well after the WAKE budget would have expired.
		if time.Since(start) < 500*time.Millisecond {
			_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
				"status": map[string]any{"completed": false}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
			"status": map[string]any{"completed": true, "status_str": "success"},
			"outputs": map[string]any{"save": map[string]any{"images": []map[string]any{
				{"filename": "af-sdxl_00001_.png"}}}},
		}})
	})
	mux.HandleFunc("/engine/image/v1/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG(t, 1, 1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &comfyProvider{
		client: srv.Client(),
		lookup: func(context.Context) (EngineConn, bool) {
			c := sdxlConn()
			c.BaseURL, c.Token = srv.URL+"/engine/image/v1", "afe_test"
			return c, true
		},
	}
	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err != nil {
		t.Fatalf("Generate() = %v — the wake budget must not be what the generation spends", err)
	}
	if len(res.Images) != 1 {
		t.Errorf("images = %d, want 1", len(res.Images))
	}
}

// Running out of time AFTER the engine accepted the prompt is not a plain failure: nothing was
// interrupted, the GPU is still working and still billing, and ComfyUI will hand the picture over
// to the identical request (measured: 1.07 s from its cache, 22 s after a 960 s timeout). The
// message has to say so, and it has to name the prompt — a caller who is only told "timed out"
// pays for a picture they never collect.
func TestComfyTimeoutAfterSubmitSaysThePictureCanStillBeCollected(t *testing.T) {
	restore := comfyShortClocks(t, 5*time.Second, 80*time.Millisecond)
	defer restore()

	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{}) // never finishes
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("Generate() = nil, want the run budget to expire")
	}
	for _, want := range []string{"af-test-prompt", "was not interrupted", "collects it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// The negative control for the message above: a timeout that happened while the box was BEING
// REPLACED says so instead. The two must not collapse into one sentence — there the work really
// may be gone (a restarted ComfyUI holds no history for the previous process's prompt id), and
// telling that caller to ask again "to collect it" would be advice to wait for nothing.
func TestComfyTimeoutDuringAWakeStillNamesTheWake(t *testing.T) {
	restore := comfyShortClocks(t, 5*time.Second, 80*time.Millisecond)
	defer restore()

	p := comfyPollStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "engine_waking", "message": "the image engine is starting"}})
	})
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox"})
	if err == nil {
		t.Fatal("Generate() = nil, want the run budget to expire")
	}
	if !strings.Contains(err.Error(), "still starting") {
		t.Errorf("error = %q, want the wake's own explanation", err)
	}
	if strings.Contains(err.Error(), "collects it") {
		t.Errorf("error = %q, want no collect-it advice: a replaced box may hold nothing", err)
	}
}

// comfyShortClocks shrinks the two budgets and the poll interval for one test, and answers the
// restore. A helper rather than three t.Cleanup lines per test because forgetting one of them
// leaves a millisecond poll interval behind for every test that runs after it.
func comfyShortClocks(t *testing.T, wake, run time.Duration) func() {
	t.Helper()
	oldWake, oldRun, oldPoll := engineTimeout, engineRunTimeout, comfyPollEvery
	engineTimeout, engineRunTimeout, comfyPollEvery = wake, run, time.Millisecond
	return func() { engineTimeout, engineRunTimeout, comfyPollEvery = oldWake, oldRun, oldPoll }
}
