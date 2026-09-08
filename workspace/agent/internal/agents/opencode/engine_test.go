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

// The window the engine was started with has to reach opencode as a `limit`, and BOTH halves
// of it. Measured against opencode 1.18.29:
//
//   - a model declared with only a `name` comes back as limit={context:0,output:0}, and
//     opencode switches auto-compaction OFF when the context is 0. The session then runs
//     until llama-server rejects the request, with nothing in the UI to explain it;
//   - the usable window is context MINUS the output cap, and 0 there is not "unset" — it
//     substitutes 32000. A 32,768-token engine declared without the output half would be
//     left with 768 usable tokens, i.e. compaction thrashing from the first turn.
func TestWriteEngineProvidersDeclaresTheWindow(t *testing.T) {
	engineTestHome(t)
	if _, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1",
		Models: []string{"qwen3-coder-30b-a3b"}, ContextTokens: 32768, MaxOutputTokens: 4096,
	}}); err != nil {
		t.Fatal(err)
	}
	root := readEngineConfig(t)
	p, _ := root["provider"].(map[string]any)
	entry, _ := p["llamacpp"].(map[string]any)
	models, _ := entry["models"].(map[string]any)
	model, _ := models["qwen3-coder-30b-a3b"].(map[string]any)
	limit, _ := model["limit"].(map[string]any)
	if limit == nil {
		t.Fatalf("no limit on the model: %v", model)
	}
	if limit["context"] != float64(32768) || limit["output"] != float64(4096) {
		t.Errorf("limit = %v", limit)
	}
}

// Half a pair is not half a fix. A stack that declares one number and not the other gets
// neither, which leaves today's behaviour (no limit at all) rather than the 768-token window
// a context without an output cap produces.
func TestWriteEngineProvidersOmitsAHalfDeclaredWindow(t *testing.T) {
	engineTestHome(t)
	for _, e := range []EngineProvider{
		{Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/x", Models: []string{"m"}},
		{Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/x", Models: []string{"m"}, ContextTokens: 32768},
		{Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/x", Models: []string{"m"}, MaxOutputTokens: 4096},
	} {
		if _, err := WriteEngineProviders([]EngineProvider{e}); err != nil {
			t.Fatal(err)
		}
		root := readEngineConfig(t)
		p, _ := root["provider"].(map[string]any)
		entry, _ := p["llamacpp"].(map[string]any)
		models, _ := entry["models"].(map[string]any)
		model, _ := models["m"].(map[string]any)
		if model == nil {
			t.Fatalf("the model went missing: %v", entry)
		}
		if _, ok := model["limit"]; ok {
			t.Errorf("ctx=%d out=%d wrote a limit anyway: %v", e.ContextTokens, e.MaxOutputTokens, model)
		}
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

// Two models, two windows — the shape ADR 0072's catalogue makes ordinary and ADR 0071's
// engine-wide pair could not express at all.
//
// Under ADR 0071 the window was the ENGINE's (one llama-server, one GGUF, one -c), so two models
// with different windows had to be two engines with two provider ids. In router mode each model
// carries its own `c` into the preset, so the engine-wide number is only a fallback — and it has
// to stay one: a model the catalogue says nothing about must not silently inherit another
// model's context, and a model it does describe must not be overwritten by the engine's.
func TestWriteEngineProvidersDeclaresAWindowPerModel(t *testing.T) {
	engineTestHome(t)
	if _, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp", BaseURL: "https://cp/engine/llm/v1",
		Models:        []string{"qwen3-coder-30b-a3b", "qwen2.5-coder-1.5b", "undescribed"},
		ContextTokens: 32768, MaxOutputTokens: 4096,
		Windows: map[string]EngineModelWindow{
			"qwen3-coder-30b-a3b": {ContextTokens: 32768, MaxOutputTokens: 4096},
			"qwen2.5-coder-1.5b":  {ContextTokens: 8192, MaxOutputTokens: 2048},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	root := readEngineConfig(t)
	p, _ := root["provider"].(map[string]any)
	entry, _ := p["llamacpp"].(map[string]any)
	models, _ := entry["models"].(map[string]any)
	for id, want := range map[string][2]float64{
		"qwen3-coder-30b-a3b": {32768, 4096},
		"qwen2.5-coder-1.5b":  {8192, 2048},
		"undescribed":         {32768, 4096}, // falls back to the engine-wide pair
	} {
		model, _ := models[id].(map[string]any)
		limit, _ := model["limit"].(map[string]any)
		if limit == nil {
			t.Errorf("%s has no limit: %v", id, model)
			continue
		}
		if limit["context"] != want[0] || limit["output"] != want[1] {
			t.Errorf("%s limit = %v, want context %v output %v", id, limit, want[0], want[1])
		}
	}
}
