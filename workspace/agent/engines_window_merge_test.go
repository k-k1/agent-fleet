package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// A stopped engine and a warm one write the same limit for the same model: the warm merge may
// not hand engineProviderEntry an output cap that no longer fits under the measured window
// (engine output 9000 against a model window of 8192 once wrote 8192/9000).
func TestWarmWindowMergeKeepsTheOutputCapUnderTheWindow(t *testing.T) {
	for _, c := range []struct {
		name    string
		e       engineCatalogRow
		id      string
		nctx    int
		wantOut float64
	}{
		{"model row, engine cap too big", engineCatalogRow{Key: "llm", Models: []string{"m"}, ContextTokens: 32768, MaxOutputTokens: 9000,
			ModelRows: []engineCatalogModel{{ID: "m", ContextTokens: 8192}}}, "m", 8192, 2048},
		{"model row, engine cap fits", engineCatalogRow{Key: "llm", Models: []string{"m"}, ContextTokens: 32768, MaxOutputTokens: 512,
			ModelRows: []engineCatalogModel{{ID: "m", ContextTokens: 8192}}}, "m", 8192, 512},
		{"no model row, engine cap too big", engineCatalogRow{Key: "llm", Models: []string{"m"}, ContextTokens: 32768, MaxOutputTokens: 9000}, "", 8192, 2048},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		ctx, out, w := mergeWarmWindow(c.e, c.id, c.nctx)
		if _, _, err := opencode.WriteEngineProviders([]opencode.EngineProvider{{
			Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/x", Models: c.e.Models,
			ContextTokens: ctx, MaxOutputTokens: out, Windows: w,
		}}); err != nil {
			t.Fatal(err)
		}
		var root struct {
			Provider map[string]struct {
				Models map[string]struct {
					Limit map[string]float64 `json:"limit"`
				} `json:"models"`
			} `json:"provider"`
		}
		var found bool
		for _, name := range []string{"opencode.jsonc", "opencode.json"} {
			if b, err := os.ReadFile(filepath.Join(paths.OpencodeConfigDir(), name)); err == nil {
				if err := json.Unmarshal(b, &root); err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: no config written", c.name)
		}
		l := root.Provider["llamacpp"].Models["m"].Limit
		if l["context"] != float64(c.nctx) || l["output"] != c.wantOut {
			t.Errorf("%s: warm limit = %v, want %d/%v", c.name, l, c.nctx, c.wantOut)
		}
	}
}
