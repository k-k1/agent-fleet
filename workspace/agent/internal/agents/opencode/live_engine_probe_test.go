//go:build clicontract

// Tier B, like live_contract_test.go: this one shells out to the REAL opencode binary, which
// is not on every machine and is not what the unit tests are for. It is the launch-menu half
// of ADR 0071 P0's definition of done, and the only way to check it is to ask the CLI.
//
//	go test -tags clicontract -run TestLiveOpencodeListsTheEngineModel ./internal/agents/opencode/
package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The launch-menu half of ADR 0071 P0's definition of done, against the real CLI: the
// provider block WriteEngineProviders emits has to make `opencode models` list
// llamacpp/<model> — WITHOUT the engine existing, which is the whole point of an engine that
// is asleep until somebody asks for it.
func TestLiveOpencodeListsTheEngineModel(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("no opencode on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if _, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp",
		BaseURL: "https://af.example.invalid/engine/llm/v1",
		Models:  []string{"qwen3-coder-30b-a3b"},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := engineConfigPath()
	b, _ := os.ReadFile(cfg)
	t.Logf("config af wrote (%s):\n%s", cfg, b)

	cmd := exec.Command("opencode", "models")
	cmd.Env = append(os.Environ(), "HOME="+home, "OPENCODE_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode models: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "llamacpp/qwen3-coder-30b-a3b") {
		t.Fatalf("llamacpp/qwen3-coder-30b-a3b is not in the launch list:\n%s", out)
	}
	t.Logf("listed: %s", firstMatching(string(out), "llamacpp/"))
}

func firstMatching(s, pre string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), pre) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}
