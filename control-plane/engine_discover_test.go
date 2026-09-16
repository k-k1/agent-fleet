package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeComfyObjectInfo answers ComfyUI's /object_info/<node> for exactly the three loaders
// engine_discover.go asks about, with the enum shape a real installation answers:
// {"<node>":{"input":{"required":{"<field>":[[...names...],{}]}}}}.
func fakeComfyObjectInfo(t *testing.T, byNode map[string][]string) *httptest.Server {
	t.Helper()
	field := map[string]string{
		"CheckpointLoaderSimple": "ckpt_name",
		"LoraLoader":             "lora_name",
		"VAELoader":              "vae_name",
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := strings.TrimPrefix(r.URL.Path, "/object_info/")
		names, ok := byNode[node]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f := field[node]
		body := map[string]any{
			node: map[string]any{
				"input": map[string]any{
					"required": map[string]any{
						f: []any{names, map[string]any{}},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func newTestExternalComfyEngine(url string) *engineRuntimeState {
	return &engineRuntimeState{
		def: engineDef{
			Key: "comfy-lan", API: engineAPIImages, URL: url,
			Provider: "comfy", Lifecycle: engineLifecycleExternal,
		},
	}
}

// The candidates ARE the filenames ComfyUI itself enumerates, a checkpoint whose name matches
// the family table carries a SUGGESTION, and nothing here writes a catalogue row at all — the
// probe alone must not be able to create the row that decision 6 says only a person may
// (ADR 0082 decision 6, ADR 0072 decision 2).
func TestEngineDiscoverProbeOffersCandidatesWithoutWritingTheCatalogue(t *testing.T) {
	up := fakeComfyObjectInfo(t, map[string][]string{
		"CheckpointLoaderSimple": {"sdxl-base-1.0.safetensors", "unknown-arch-9000.safetensors"},
		"LoraLoader":             {"illustrious-detail.safetensors"},
		"VAELoader":              {"sdxl-vae.safetensors"},
	})
	defer up.Close()

	e := newTestExternalComfyEngine(up.URL)
	st := testSettingsStore(t)
	e.catalog = newEngineCatalog(st, "comfy-lan")

	res, err := e.discoverModels(context.Background())
	if err != nil {
		t.Fatalf("discoverModels = %v, want no error", err)
	}
	if len(res.Checkpoints) != 2 {
		t.Fatalf("checkpoints = %#v, want 2", res.Checkpoints)
	}
	// Sorted by name: "sdxl-base-1.0.safetensors" < "unknown-arch-9000.safetensors".
	if res.Checkpoints[0].Name != "sdxl-base-1.0.safetensors" {
		t.Errorf("checkpoints[0].Name = %q", res.Checkpoints[0].Name)
	}
	if res.Checkpoints[0].BaseModelSuggest != "sdxl" {
		t.Errorf("checkpoints[0].BaseModelSuggest = %q, want sdxl", res.Checkpoints[0].BaseModelSuggest)
	}
	// A filename the family table does not recognise gets no suggestion at all — never a
	// guess, and never the empty string standing in for one (ADR 0072 decision 2).
	if res.Checkpoints[1].BaseModelSuggest != "" {
		t.Errorf("checkpoints[1].BaseModelSuggest = %q, want empty (unrecognised architecture)", res.Checkpoints[1].BaseModelSuggest)
	}
	if len(res.Loras) != 1 || res.Loras[0].Name != "illustrious-detail.safetensors" {
		t.Errorf("loras = %#v", res.Loras)
	}
	// A LoRA candidate carries no suggestion at all: base_model there is a future compatibility
	// target, not a workflow dispatch key, and this probe only ever answers the dispatch
	// question (ADR 0072 decision 2's own commentary on the LoRA case).
	if res.Loras[0].BaseModelSuggest != "" {
		t.Errorf("loras[0].BaseModelSuggest = %q, want empty", res.Loras[0].BaseModelSuggest)
	}
	if len(res.Vaes) != 1 || res.Vaes[0].Name != "sdxl-vae.safetensors" {
		t.Errorf("vaes = %#v", res.Vaes)
	}

	// Nothing was ever written. The probe has to be incapable of creating a row, not merely
	// disinclined to.
	rows, err := st.ListEngineModels(context.Background(), "comfy-lan")
	if err != nil {
		t.Fatalf("ListEngineModels: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the probe wrote %d catalogue row(s); it must write none", len(rows))
	}
}

// A host that never answers gets a bounded wait, not the gateway's 5-second health budget —
// this runs on the admin panel's own synchronous list handler (ADR 0076 decision 8, reused by
// ADR 0082 decision 7), and the message is the same sentence a generation would have been
// refused with.
func TestEngineDiscoverProbeGivesUpWithinBudgetWhenTheHostIsDown(t *testing.T) {
	block := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never answers within the probe's own timeout
	}))
	// 🔴 Order matters: httptest.Server.Close() waits for outstanding connections, and the
	// handler above only releases them once `block` closes — deferred in the wrong order this
	// deadlocks the test itself rather than the engine. `block` must close FIRST.
	defer up.Close()
	defer close(block)

	e := newTestExternalComfyEngine(up.URL)
	st := testSettingsStore(t)
	e.catalog = newEngineCatalog(st, "comfy-lan")

	start := time.Now()
	_, err := e.discoverModels(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("discoverModels = nil error, want the host-down refusal")
	}
	if !strings.Contains(err.Error(), "is not answering") {
		t.Errorf("error = %q, want the same sentence ensureStarted refuses a generation with", err.Error())
	}
	// engineExternalWarmTimeout is 2s; three loaders run CONCURRENTLY, so the whole probe must
	// not cost anywhere near 3x that — generous slack for a loaded CI host, but well short of
	// the gateway's own 5-second health budget this must not reuse.
	if elapsed > 4*time.Second {
		t.Errorf("discoverModels blocked for %s against a host that never answers; want well under the gateway's 5s health budget", elapsed)
	}
}

// Pressing the button twice inside the cache window must not cost the LAN box a second round
// trip (ADR 0076 decision 8's 10-second TTL, reused by ADR 0082 decision 7).
func TestEngineDiscoverProbeCachesWithinTTL(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		node := strings.TrimPrefix(r.URL.Path, "/object_info/")
		f := map[string]string{"CheckpointLoaderSimple": "ckpt_name", "LoraLoader": "lora_name", "VAELoader": "vae_name"}[node]
		_ = json.NewEncoder(w).Encode(map[string]any{
			node: map[string]any{"input": map[string]any{"required": map[string]any{f: []any{[]string{}, map[string]any{}}}}},
		})
	}))
	defer up.Close()

	e := newTestExternalComfyEngine(up.URL)
	st := testSettingsStore(t)
	e.catalog = newEngineCatalog(st, "comfy-lan")

	if _, err := e.discoverModels(context.Background()); err != nil {
		t.Fatalf("first discoverModels: %v", err)
	}
	first := hits
	if _, err := e.discoverModels(context.Background()); err != nil {
		t.Fatalf("second discoverModels: %v", err)
	}
	if hits != first {
		t.Errorf("a second press within the TTL cost %d more upstream requests, want 0 (cached)", hits-first)
	}
}

// The HTTP route: a borrowed (remote) row's catalogue is somebody else's read-only mirror, and
// there is nothing on THIS deployment's network to discover behind it (ADR 0079 decision 7,
// ADR 0082 decision 7).
func TestDiscoverModelsRouteRefusesABorrowedRow(t *testing.T) {
	e := &engineRuntimeState{def: engineDef{
		Key: "image", API: engineAPIImages, Provider: "comfy",
		Lifecycle: engineLifecycleRemote, URL: "https://far.invalid",
	}}
	st := testSettingsStore(t)
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/discover", nil)
	r.SetPathValue("key", "image")
	a.discoverModelsGrant(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeEngineNotOurs) {
		t.Fatalf("discover on a borrowed row = %d (%s), want 400 %s", rec.Code, rec.Body.String(), errCodeEngineNotOurs)
	}
}

// A row this button was never for — here, a managed (in-stack) comfy engine rather than an
// external one — is refused rather than dialled: probing it must never be the thing that wakes
// a GPU role (ADR 0082 decision 7).
func TestDiscoverModelsRouteRefusesAManagedRow(t *testing.T) {
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	st := testSettingsStore(t)
	e.settings, e.catalog = st, newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/discover", nil)
	r.SetPathValue("key", "image")
	a.discoverModelsGrant(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeEngineDiscoverUnsupported) {
		t.Fatalf("discover on a managed row = %d (%s), want 400 %s", rec.Code, rec.Body.String(), errCodeEngineDiscoverUnsupported)
	}
}

// The route end to end, on an external row that answers: what the button gets back is exactly
// what the probe answers, on the wire.
func TestDiscoverModelsRouteSucceedsOnAnExternalComfyRow(t *testing.T) {
	up := fakeComfyObjectInfo(t, map[string][]string{
		"CheckpointLoaderSimple": {"flux1-dev.safetensors"},
		"LoraLoader":             {},
		"VAELoader":              {},
	})
	defer up.Close()

	e := newTestExternalComfyEngine(up.URL)
	st := testSettingsStore(t)
	e.catalog = newEngineCatalog(st, "comfy-lan")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"comfy-lan": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/comfy-lan/discover", nil)
	r.SetPathValue("key", "comfy-lan")
	a.discoverModelsGrant(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})

	if rec.Code != http.StatusOK {
		t.Fatalf("discover on an external row = %d (%s)", rec.Code, rec.Body.String())
	}
	var out engineDiscoverResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Checkpoints) != 1 || out.Checkpoints[0].Name != "flux1-dev.safetensors" {
		t.Fatalf("checkpoints = %#v", out.Checkpoints)
	}
	if out.Checkpoints[0].BaseModelSuggest != "flux1" {
		t.Errorf("BaseModelSuggest = %q, want flux1", out.Checkpoints[0].BaseModelSuggest)
	}
}
