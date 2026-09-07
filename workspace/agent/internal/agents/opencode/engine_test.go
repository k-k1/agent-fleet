package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func engineTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// The launch menu has to show the model while the GPU box is asleep, which is the whole
// reason the model ids are declared by the stack rather than read from the engine (ADR 0071
// decision 2). What that costs on this side is a config block opencode can list from —
// npm provider, baseURL, and an apiKey that is an {env:…} indirection rather than a value.
func TestWriteEngineProvidersDeclaresTheModelWithoutTheEngine(t *testing.T) {
	engineTestHome(t)
	changed, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp",
		BaseURL: "https://cp.example.com/engine/llm/v1/",
		Models:  []string{"qwen3-coder-30b-a3b"},
	}})
	if err != nil || !changed {
		t.Fatalf("write: changed=%v err=%v", changed, err)
	}
	root := readEngineConfig(t)
	p, _ := root["provider"].(map[string]any)
	entry, _ := p["llamacpp"].(map[string]any)
	if entry == nil {
		t.Fatalf("no llamacpp provider in %v", root)
	}
	if entry["npm"] != "@ai-sdk/openai-compatible" {
		t.Errorf("npm = %v", entry["npm"])
	}
	opts, _ := entry["options"].(map[string]any)
	// The trailing slash has to go: opencode appends /chat/completions and a double slash
	// is a 404 nobody would connect to a trailing character in a CloudFormation output.
	if opts["baseURL"] != "https://cp.example.com/engine/llm/v1" {
		t.Errorf("baseURL = %v", opts["baseURL"])
	}
	// The key is an indirection, never a value: the file is shared by every session in the
	// workspace, and the credential is per session.
	if opts["apiKey"] != "{env:AF_ENGINE_TOKEN}" {
		t.Errorf("apiKey = %v — a literal key here would be one credential for every session", opts["apiKey"])
	}
	models, _ := entry["models"].(map[string]any)
	if _, ok := models["qwen3-coder-30b-a3b"]; !ok {
		t.Errorf("models = %v", models)
	}
}

// The config is the USER's file. af owns its own provider entries and nothing else — the
// same bargain the MCP materializer makes — so a hand-written provider and a hand-written
// top-level member both survive, and af's own stale entry does not.
func TestWriteEngineProvidersLeavesTheUsersOwnAlone(t *testing.T) {
	engineTestHome(t)
	path := filepath.Join(mustConfigDir(t), configNames[0])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"$schema":"https://opencode.ai/config.json","theme":"opencode",
	  "provider":{"mine":{"npm":"@ai-sdk/openai-compatible"}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1",
		Models: []string{"m"},
	}}); err != nil {
		t.Fatal(err)
	}
	root := readEngineConfig(t)
	if root["theme"] != "opencode" {
		t.Error("a top-level member the user set was dropped")
	}
	p, _ := root["provider"].(map[string]any)
	if _, ok := p["mine"]; !ok {
		t.Error("the user's own provider was removed")
	}

	// The engine goes away (the stack was taken down). af's entry must go with it, or the
	// launch menu keeps offering a model whose gateway now answers 404.
	if _, err := WriteEngineProviders(nil); err != nil {
		t.Fatal(err)
	}
	root = readEngineConfig(t)
	p, _ = root["provider"].(map[string]any)
	if _, ok := p["llamacpp"]; ok {
		t.Error("af's own stale provider survived")
	}
	if _, ok := p["mine"]; !ok {
		t.Error("removing af's provider took the user's with it")
	}
}

// A config af cannot parse is refused, never rewritten. opencode.jsonc may legally carry
// comments, and reformatting one away costs the user whatever af could not read.
func TestWriteEngineProvidersRefusesAnUnparseableConfig(t *testing.T) {
	engineTestHome(t)
	path := filepath.Join(mustConfigDir(t), configNames[0])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "{\n  // a comment jsonc allows\n  \"theme\": \"opencode\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1", Models: []string{"m"},
	}})
	if err == nil {
		t.Fatal("an unparseable config was accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("the file was rewritten anyway:\n%s", got)
	}
}

// Every workspace start runs this. Writing an identical file each time would churn a file
// opencode itself also writes, and widen the window where af's write races the CLI's.
func TestWriteEngineProvidersIsANoOpWhenNothingChanged(t *testing.T) {
	engineTestHome(t)
	engines := []EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1",
		Models: []string{"b", "a"},
	}}
	if changed, err := WriteEngineProviders(engines); err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	if changed, err := WriteEngineProviders(engines); err != nil || changed {
		t.Fatalf("second write: changed=%v err=%v — an unchanged catalogue must not touch the file", changed, err)
	}
	// Model order comes off a CommaDelimitedList and is not guaranteed; sorting is what
	// makes "unchanged" mean unchanged.
	if changed, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1",
		Models: []string{"a", "b"},
	}}); err != nil || changed {
		t.Fatalf("reordered models: changed=%v err=%v", changed, err)
	}
}

// An engine the stack declared with no models is not something a person can pick, and
// writing it would put an empty provider in the launch menu.
func TestWriteEngineProvidersSkipsAnEngineWithNoModels(t *testing.T) {
	engineTestHome(t)
	changed, err := WriteEngineProviders([]EngineProvider{{Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a provider with no models was written")
	}
	if _, err := os.Stat(filepath.Join(mustConfigDir(t), configNames[0])); !os.IsNotExist(err) {
		t.Error("a config file was conjured for an engine with nothing in it")
	}
}

// 🔥 The regression that produced `401 invalid engine session token` on the first message of
// a managed session, on the live deployment.
//
// opencode's MANAGED route — the default one — runs every session through a single shared
// `opencode serve` daemon, and that daemon's environment is exactly what env() returns
// (serve.go). A token placed only on LaunchPlan.Env reaches the tmux route and nothing else,
// so `{env:AF_ENGINE_TOKEN}` resolved to nothing and the gateway refused it. Nothing about the
// launch menu looked wrong — the model was listed and selectable — which is why this needs a
// test of its own rather than being obvious from the config one above.
func TestEngineTokenReachesTheSharedServeDaemonEnv(t *testing.T) {
	engineTestHome(t)
	prevUsage, prevEnv := UsagePref, EngineEnv
	t.Cleanup(func() { UsagePref, EngineEnv = prevUsage, prevEnv })
	UsagePref = func() string { return UsageFree }

	var asked []string
	EngineEnv = func(session string) []string {
		asked = append(asked, session)
		return []string{EngineProviderKeyEnv + "=afe_workspace"}
	}
	got := env()
	if len(asked) != 1 || asked[0] != "" {
		t.Fatalf("EngineEnv called with %v, want exactly one workspace-scoped ask — a daemon has no session", asked)
	}
	var found string
	for _, e := range got {
		if strings.HasPrefix(e, EngineProviderKeyEnv+"=") {
			found = e
		}
	}
	if found != EngineProviderKeyEnv+"=afe_workspace" {
		t.Fatalf("env() = %v — without the token here, every managed session gets 401", got)
	}
}

func readEngineConfig(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(engineConfigPath())
	if err != nil {
		t.Fatalf("reading the config: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("the config af wrote is not JSON: %v\n%s", err, b)
	}
	return root
}

func mustConfigDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Dir(engineConfigPath())
	if !strings.Contains(dir, "opencode") {
		t.Fatalf("config dir %q does not look like opencode's", dir)
	}
	return dir
}
