package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func engineUsageHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

func postEngineUsage(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/engine/usage", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	handleEngineUsage(rec, r)
	return rec
}

func readLedger(t *testing.T) []usagex.Record {
	t.Helper()
	dir := filepath.Join(paths.AgentDataDir(), "usage", "raw")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no ledger directory: %v", err)
	}
	var rows []usagex.Record
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var r usagex.Record
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("ledger line is not a Record: %v (%s)", err, line)
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// The gateway is the only party that sees an engine's response, so it counts the tokens and
// posts them here — and the row has to land in the SAME ledger every other feature writes
// to, cut by session, or the usage graph simply has no engine column (ADR 0071 decision 9).
func TestEngineUsageLandsInTheLedger(t *testing.T) {
	engineUsageHome(t)
	rec := postEngineUsage(t, `{"feature":"engine.llm","provider":"llamacpp","session":"sess-a",
	  "model":"qwen3-coder-30b-a3b","in":23226,"out":57,"ms":12100,"ok":true,"measured":"exact"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	rows := readLedger(t)
	if len(rows) != 1 {
		t.Fatalf("ledger has %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Feature != usagex.FeatureEngineLLM || r.Ref != "sess-a" {
		t.Errorf("feature/ref = %q/%q", r.Feature, r.Ref)
	}
	if r.In != 23226 || r.Out != 57 || r.Spend != 23283 {
		t.Errorf("in/out/spend = %d/%d/%d", r.In, r.Out, r.Spend)
	}
	if r.Measured != usagex.MeasuredExact {
		t.Errorf("measured = %q, want exact", r.Measured)
	}
	if r.Model != "qwen3-coder-30b-a3b" {
		t.Errorf("model = %q", r.Model)
	}
}

// An engine that reported nothing and an engine that reported zero spent are different
// facts. Writing 0/0 as "exact" would put free calls into the graph, which is the same
// failure mode ADR 0069 avoided for images by leaving them unmeasured rather than zeroed.
func TestEngineUsageUnreportedIsMeasuredNone(t *testing.T) {
	engineUsageHome(t)
	rec := postEngineUsage(t, `{"feature":"engine.llm","session":"s","in":0,"out":0,"ok":true,"measured":"exact"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	rows := readLedger(t)
	if len(rows) != 1 || rows[0].Measured != usagex.MeasuredNone {
		t.Fatalf("measured = %q, want none", rows[0].Measured)
	}
}

// The feature enumeration is frozen (ADR 0029 §2) and the Console translates it by exact
// key. A row whose feature is whatever the caller said would show up as an untranslated
// string in somebody's usage graph, so the route refuses it rather than storing it.
func TestEngineUsageRefusesAForeignFeature(t *testing.T) {
	engineUsageHome(t)
	for _, body := range []string{
		`{"feature":"assistant.chat","session":"s","in":1,"out":1}`,
		`{"feature":"","session":"s","in":1,"out":1}`,
	} {
		if rec := postEngineUsage(t, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.AgentDataDir(), "usage", "raw")); err == nil {
		t.Error("a refused row still created the ledger")
	}
}

// No CP to ask means no engines, and everything on this side is a no-op — not an error, and
// not a launch that fails. This is what most deployments do.
func TestEngineSessionEnvIsEmptyWithoutAControlPlane(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "")
	if got := engineSessionEnv("sess-a"); got != nil {
		t.Fatalf("engineSessionEnv = %v, want nil", got)
	}
}

// The token is bought per SESSION and cached, so a relaunch does not leave a trail of live
// credentials for one session — and the name in it is what the usage row is cut by.
func TestEngineSessionEnvMintsPerSessionAndCaches(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/engine/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct{ Session string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Session)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "afe_" + req.Session, "expires_at": "2099-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "afei_test")
	engineTokenCache.Clear()

	got := engineSessionEnv("sess-a")
	if len(got) != 1 || got[0] != "AF_ENGINE_TOKEN=afe_sess-a" {
		t.Fatalf("env = %v", got)
	}
	if second := engineSessionEnv("sess-b"); len(second) != 1 || second[0] != "AF_ENGINE_TOKEN=afe_sess-b" {
		t.Fatalf("a second session got %v — the token is per session", second)
	}
	if again := engineSessionEnv("sess-a"); len(again) != 1 || again[0] != "AF_ENGINE_TOKEN=afe_sess-a" {
		t.Fatalf("cached read = %v", again)
	}
	if len(asked) != 2 {
		t.Fatalf("the CP was asked %d times for 3 launches (%v) — the cache is not holding", len(asked), asked)
	}
}

// --- the catalogue, and the two things it feeds (ADR 0071 P1) --------------------

// engineCatalogStub answers /internal/engine/catalog with `rows` and mints a token per ask,
// echoing back which engine was asked for. It resets both caches, which are process-global.
func engineCatalogStub(t *testing.T, rows string) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/engine/catalog":
			_, _ = w.Write([]byte(`{"engines":[` + rows + `]}`))
		case "/internal/engine/token":
			var req struct{ Session, Key string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			asked = append(asked, req.Key+"/"+req.Session)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "afe_" + req.Key, "expires_at": "2099-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_ENGINE_ISSUE_TOKEN", "afei_test")
	engineTokenCache.Clear()
	engineCatalogState.mu.Lock()
	engineCatalogState.rows, engineCatalogState.at, engineCatalogState.ok = nil, time.Time{}, false
	engineCatalogState.mu.Unlock()
	t.Cleanup(func() {
		engineCatalogState.mu.Lock()
		engineCatalogState.rows, engineCatalogState.at, engineCatalogState.ok = nil, time.Time{}, false
		engineCatalogState.mu.Unlock()
		engineTokenCache.Clear()
	})
	return &asked
}

const engineRowLlm = `{"key":"llm","api":"chat","provider":"llamacpp","base_url":"/engine/llm/v1","models":["qwen3-coder-30b-a3b"]}`
const engineRowImage = `{"key":"image","api":"images","provider":"sdcpp","base_url":"/engine/image/v1","models":["sdxl-base-1.0"]}`

const engineRowLlmSized = `{"key":"llm","api":"chat","provider":"llamacpp","base_url":"/engine/llm/v1","models":["qwen3-coder-30b-a3b"],"context_tokens":32768,"max_output_tokens":4096}`

// The window the stack started llama-server with reaches opencode's config as a `limit`, or
// the model is listed with a context of 0 — which is how opencode reads a model it has never
// heard of, and it turns auto-compaction off at 0.
func TestSyncEngineProvidersCarriesTheDeclaredWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	engineCatalogStub(t, engineRowLlmSized)

	syncEngineProviders()

	b, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.jsonc"))
	if err != nil {
		t.Fatalf("no opencode config written: %v", err)
	}
	var cfg struct {
		Provider map[string]struct {
			Models map[string]struct {
				Limit struct{ Context, Output int } `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Provider["llamacpp"].Models["qwen3-coder-30b-a3b"].Limit
	if got.Context != 32768 || got.Output != 4096 {
		t.Errorf("limit = %+v\n%s", got, b)
	}
}

// The image engine must not become an opencode provider. It answers /v1/images/generations
// and nothing else, so `sdcpp/sdxl-base-1.0` in the launch menu would be a model you can
// pick and then cannot talk to.
func TestSyncEngineProvidersWritesOnlyChatEngines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	engineCatalogStub(t, engineRowLlm+","+engineRowImage)

	syncEngineProviders()

	b, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.jsonc"))
	if err != nil {
		t.Fatalf("no opencode config written: %v", err)
	}
	var cfg struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Provider["llamacpp"]; !ok {
		t.Errorf("the chat engine is missing from %v", cfg.Provider)
	}
	if _, ok := cfg.Provider["sdcpp"]; ok {
		t.Error("the image engine was written as a chat provider — it would appear in the launch menu")
	}
}

// The image tool's transport: the images engine, an absolute URL through the CP, and a token
// minted for THAT engine (a token carries the engine it opens, and the gateway refuses one
// presented at another).
func TestEngineImageConnFindsTheImagesEngine(t *testing.T) {
	asked := engineCatalogStub(t, engineRowLlm+","+engineRowImage)
	base := os.Getenv("AF_CP_BASE_URL")

	conn, ok := engineImageConn(context.Background())
	if !ok {
		t.Fatal("no image engine found in a catalogue that has one")
	}
	if conn.BaseURL != base+"/engine/image/v1" {
		t.Errorf("base url = %q", conn.BaseURL)
	}
	if conn.Token != "afe_image" || len(*asked) != 1 || (*asked)[0] != "image/" {
		t.Errorf("token = %q, asks = %v — want one workspace-scoped ask for the image engine",
			conn.Token, *asked)
	}
	if len(conn.Models) != 1 || conn.Models[0] != "sdxl-base-1.0" {
		t.Errorf("models = %v", conn.Models)
	}
	// A second call is served from the caches: this sits behind Ready(), which a client calls
	// on every tools/list.
	if _, _ = engineImageConn(context.Background()); len(*asked) != 1 {
		t.Errorf("asks after a second lookup = %v — the token cache is not holding", *asked)
	}
}

// A deployment with only the llm role — which is every deployment until an image checkpoint
// is staged — has no image provider, and that is a quiet no, not an error.
func TestEngineImageConnIsAbsentWithoutAnImageEngine(t *testing.T) {
	engineCatalogStub(t, engineRowLlm)
	if _, ok := engineImageConn(context.Background()); ok {
		t.Fatal("found an image engine in a catalogue that has none")
	}
}

// One token per (engine, scope). Presenting the llm token to /engine/image/v1 is refused by
// the gateway — the claim doing its job — so the cache must not hand the same value to both.
func TestEngineTokenIsCachedPerEngine(t *testing.T) {
	asked := engineCatalogStub(t, engineRowLlm+","+engineRowImage)
	if got := engineToken(context.Background(), "llm", "sess-a"); got != "afe_llm" {
		t.Fatalf("llm token = %q", got)
	}
	if got := engineToken(context.Background(), "image", "sess-a"); got != "afe_image" {
		t.Fatalf("image token = %q — the cache is keyed by scope alone", got)
	}
	if got := engineToken(context.Background(), "llm", "sess-a"); got != "afe_llm" {
		t.Fatalf("cached llm token = %q", got)
	}
	if len(*asked) != 2 {
		t.Fatalf("asks = %v, want one per engine", *asked)
	}
}
