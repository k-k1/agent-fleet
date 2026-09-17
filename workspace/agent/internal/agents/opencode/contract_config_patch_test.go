//go:build clicontract

// Tier A, like contract_test.go: the drift alarm for PATCH /global/config, which is what
// lets an engine change reach a running daemon instead of replacing it.
//
//	cd workspace/agent && go test -tags clicontract -run TestContractConfigPatch ./internal/agents/opencode/
//
// Two facts are pinned, and the fleet reads differently if either moves:
//
//   - a patched provider is served at once, without a new process. If this stops holding,
//     engines.go is handing changes to a daemon that ignores them, and the launch menu shows
//     a model the daemon cannot route to.
//   - PATCH MERGES: it cannot remove. WriteEngineProviders reports `removed` and the caller
//     falls back to a restart purely because of this. If opencode ever gains removal, that
//     fallback — and the restart notice it raises — can go.
package opencode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func patchGlobalConfig(t *testing.T, addr string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("PATCH", addr+"/global/config", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := serveClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /global/config: %v", err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// servesProvider reports whether the daemon currently offers the provider id.
func servesProvider(t *testing.T, addr, id string) bool {
	t.Helper()
	res, err := serveClient.Get(addr + "/config/providers")
	if err != nil {
		t.Fatalf("GET /config/providers: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		Providers []struct {
			ID string `json:"id"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	for _, p := range out.Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

func TestContractConfigPatchAppliesWithoutARestart(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"$schema":"https://opencode.ai/config.json","permission":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, _ := startServeIn(t, home)

	if servesProvider(t, addr, "fleetprobe") {
		t.Fatal("the fixture must start without the provider, or this proves nothing")
	}

	entry := engineProviderEntry(EngineProvider{
		Key: "probe", Provider: "fleetprobe", BaseURL: "http://127.0.0.1:9/v1",
		Models: []string{"probe-model"}, ContextTokens: 32768, MaxOutputTokens: 4096,
	})
	if code := patchGlobalConfig(t, addr, map[string]any{"provider": map[string]any{"fleetprobe": entry}}); code >= 300 {
		t.Fatalf("PATCH answered %d", code)
	}

	// The whole point: the SAME process now serves it.
	if !servesProvider(t, addr, "fleetprobe") {
		t.Fatal("a patched provider must be served without replacing the daemon — engines.go relies on it")
	}
	// And it survives a restart, because the patch reached the file too.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("the config is no longer plain JSON after a patch: %v", err)
	}
	providers, _ := root["provider"].(map[string]any)
	if _, ok := providers["fleetprobe"]; !ok {
		t.Errorf("the patch has to persist, or the next start loses it: %s", b)
	}

	// Merge-only. Both shapes of "take it away" fail, which is why a removal still owes a
	// new process (WriteEngineProviders' `removed`).
	if code := patchGlobalConfig(t, addr, map[string]any{"provider": map[string]any{}}); code < 300 {
		if !servesProvider(t, addr, "fleetprobe") {
			t.Error("PATCH can now remove a provider: drop the restart fallback in engines.go")
		}
	}
	if code := patchGlobalConfig(t, addr, map[string]any{"provider": map[string]any{"fleetprobe": nil}}); code < 300 {
		if !servesProvider(t, addr, "fleetprobe") {
			t.Error("a null value now removes a provider: drop the restart fallback in engines.go")
		}
	}
}
