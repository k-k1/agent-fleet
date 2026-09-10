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

// Caps advertises generate only (P2 scope decision) even though sdcpp, the other self-hosted
// provider, offers edit and inpaint too.
func TestComfyCapsIsGenerateOnly(t *testing.T) {
	p, _ := comfyStub(t, sdxlConn(), nil)
	caps := p.Caps("sdxl-base-1.0")
	if len(caps.Ops) != 1 || caps.Ops[0] != OpGenerate {
		t.Errorf("ops = %v, want [generate] only", caps.Ops)
	}
	if caps.Supports(OpEdit) || caps.Supports(OpInpaint) {
		t.Error("comfy must not claim edit/inpaint yet — no template exists for either")
	}
}

// A model with no declared baseModel is refused rather than guessed at — decision 2 exists so
// the family is a stated fact, and comfy has no fallback the way sdcpp's id-sniffing guess does.
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
