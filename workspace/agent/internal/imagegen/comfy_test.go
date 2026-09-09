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
	var gotAuth string
	var gotGraph map[string]any
	p, _ := comfyStub(t, sdxlConn(), func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotAuth = r.Header.Get("Authorization")
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
func TestComfyGenerateRefusesAModelWithNoDeclaredFamily(t *testing.T) {
	conn := EngineConn{Models: []string{"mystery-model"}}
	p, _ := comfyStub(t, conn, nil)
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a fox", Model: "mystery-model"})
	if err == nil || !strings.Contains(err.Error(), "baseModel") {
		t.Errorf("err = %v, want a refusal naming the missing baseModel", err)
	}
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
