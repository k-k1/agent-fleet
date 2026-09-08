package opencode

// engine.go — the fleet's own inference engines as an opencode provider (ADR 0071 P0,
// decisions 4 and 6).
//
// Everything here is config plus one environment variable. opencode already knows how to
// talk to an OpenAI-compatible endpoint through @ai-sdk/openai-compatible, so the whole
// integration is a `provider` block in the global config:
//
//	"provider": {
//	  "llamacpp": {
//	    "npm": "@ai-sdk/openai-compatible",
//	    "options": { "baseURL": "https://<cp>/engine/llm/v1", "apiKey": "{env:AF_ENGINE_TOKEN}" },
//	    "models": { "qwen3-coder-30b-a3b": { "name": "…" } }
//	  }
//	}
//
// Two measured facts hold this up:
//
//   - `opencode models` lists a provider declared this way WITHOUT contacting it, so the
//     launch menu can be drawn while the GPU box is asleep — which is the entire point of
//     an on-demand engine. models.go needed no change at all.
//   - `{env:…}` is resolved by whichever process reads the config, and the interactive
//     session is its own process with its own tmux environment. So the key can be per
//     SESSION even though the config file is shared.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// EngineProviderKeyEnv is the environment variable the session's engine token arrives in,
// and the name the config's `{env:…}` refers to. One constant, because the two have to
// agree and a mismatch shows up as an authentication failure at the far end of a GPU start.
const EngineProviderKeyEnv = "AF_ENGINE_TOKEN"

// EngineProvider is one engine as the Agent was told about it by the Control Plane.
type EngineProvider struct {
	Key      string   // "llm"
	Provider string   // "llamacpp" — the id a model is picked as <provider>/<model>
	BaseURL  string   // absolute: the CP's public base plus /engine/<key>/v1
	Models   []string // model ids, declared by the stack (the engine is asleep)

	// The window the engine was started with (llama-server's -c) and the output cap declared
	// with it, or 0 when nobody declared them. The ENGINE-wide fallback: it describes the model
	// the engine will start with, and it is what a Control Plane older than the model catalogue
	// sends.
	ContextTokens   int
	MaxOutputTokens int

	// Windows is the per-model form (ADR 0072 decision 3). It wins over the pair above for any
	// id it names. The distinction stopped being academic with the catalogue: two models with
	// different windows used to have to be two engines with two provider ids, because there was
	// only one place to put the number.
	Windows map[string]EngineModelWindow
}

// EngineModelWindow is one model's declared context and output cap. Both or neither are
// meaningful — see engineProviderEntry for the measurement behind that.
type EngineModelWindow struct {
	ContextTokens   int
	MaxOutputTokens int
}

// engineProviderConfigKey is the top-level member af owns here. Only entries af itself
// wrote are ever removed from it, on the same "this is the USER's file" rule the MCP
// materializer works to: a provider somebody added by hand must survive.
const engineProviderConfigKey = "provider"

// engineProviderMarker is written into every entry af owns. Without it there is no way to
// tell af's `llamacpp` from a hand-written one with the same name, and "delete anything
// that looks like ours" is how a user's own configuration disappears.
const engineProviderMarker = "af-managed"

// WriteEngineProviders makes the config's provider block match `engines`, and reports
// whether anything changed.
//
// An unparseable config is REFUSED rather than overwritten — opencode.jsonc may legally
// carry comments that encoding/json cannot read, and a config af cannot read is a config af
// must not reformat away (the same bargain materialize_json.go makes).
func WriteEngineProviders(engines []EngineProvider) (changed bool, err error) {
	path := engineConfigPath()
	root := map[string]any{}
	b, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if err := json.Unmarshal(b, &root); err != nil {
			return false, fmt.Errorf("%s is not plain JSON, leaving it alone: %w", path, err)
		}
	case !os.IsNotExist(rerr):
		return false, rerr
	default:
		if len(engines) == 0 {
			return false, nil // nothing to say, so no file is conjured
		}
		root["$schema"] = "https://opencode.ai/config.json"
	}

	providers, _ := root[engineProviderConfigKey].(map[string]any)
	before, _ := json.Marshal(providers)
	if providers == nil {
		providers = map[string]any{}
	}
	want := map[string]bool{}
	for _, e := range engines {
		if e.Provider == "" || e.BaseURL == "" || len(e.Models) == 0 {
			continue // an engine with no models is not a provider anybody can pick
		}
		want[e.Provider] = true
		providers[e.Provider] = engineProviderEntry(e)
	}
	// Drop the ones af wrote on an earlier boot and no longer offers — an engine removed
	// from the stack must leave the launch menu, or picking it fails at request time with
	// a 404 nobody can act on.
	for name, v := range providers {
		if want[name] {
			continue
		}
		if m, ok := v.(map[string]any); ok && m[engineProviderMarker] == true {
			delete(providers, name)
		}
	}

	after, _ := json.Marshal(providers)
	if string(before) == string(after) && rerr == nil {
		return false, nil
	}
	// Nothing to say and no file to say it in. Conjuring one holding only a $schema line
	// would leave a config behind on every workspace that has no engines — which is most of
	// them — and opencode would then merge that empty file forever.
	if len(providers) == 0 && rerr != nil {
		return false, nil
	}
	if len(providers) == 0 {
		delete(root, engineProviderConfigKey)
	} else {
		root[engineProviderConfigKey] = providers
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return false, err
	}
	InvalidateModels()
	return true, nil
}

// engineProviderEntry is one provider as opencode reads it.
func engineProviderEntry(e EngineProvider) map[string]any {
	models := map[string]any{}
	ids := append([]string(nil), e.Models...)
	sort.Strings(ids) // stable, so a re-read is byte-identical and no-op launches do not churn the file
	for _, id := range ids {
		m := map[string]any{"name": id + " (self-hosted)"}
		// Both numbers or neither, and both measured against opencode 1.18.29 rather than
		// guessed:
		//
		//   - a model with no `limit` gets context 0, and opencode DISABLES auto-compaction
		//     when the context is 0. The session then runs until llama-server rejects the
		//     request, which is the failure this field exists to prevent;
		//   - the usable window is context MINUS the output cap, and an output of 0 is not
		//     "unset" there — it falls back to 32000. Writing the context alone would leave
		//     a 32k engine with 768 usable tokens and compaction thrashing from turn one.
		//
		// So a stack that declares only one of them gets neither: today's behaviour, rather
		// than a worse one dressed up as a fix.
		//
		// The model's own window wins over the engine's. With ADR 0072's catalogue an engine
		// can offer several models with different `-c` values, and writing the engine-wide
		// number against all of them would advertise the wrong context for every model but one.
		ctx, out := e.ContextTokens, e.MaxOutputTokens
		if w, ok := e.Windows[id]; ok && w.ContextTokens > 0 {
			ctx, out = w.ContextTokens, w.MaxOutputTokens
		}
		if ctx > 0 && out > 0 {
			m["limit"] = map[string]any{"context": ctx, "output": out}
		}
		models[id] = m
	}
	return map[string]any{
		engineProviderMarker: true,
		"npm":                "@ai-sdk/openai-compatible",
		"name":               "Agent Fleet (" + e.Key + ")",
		// The key is NOT written here. `{env:…}` is opencode's own indirection, resolved in
		// the session process, which is what lets one shared config carry a per-session
		// credential — and what keeps a long-lived secret out of a file on disk.
		"options": map[string]any{
			"baseURL": strings.TrimRight(e.BaseURL, "/"),
			"apiKey":  "{env:" + EngineProviderKeyEnv + "}",
		},
		"models": models,
	}
}

// engineConfigPath is the same file the MCP materializer edits, chosen the same way:
// opencode reads and MERGES opencode.jsonc and opencode.json, so af has to commit to
// exactly one, and it edits whichever exists (.jsonc first, which is what
// `opencode mcp add` and the image's entrypoint create).
func engineConfigPath() string {
	dir := paths.OpencodeConfigDir()
	for _, name := range configNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, configNames[0])
}

// EngineEnv is the engine credential, as a KEY=VALUE ready for an environment. An empty
// session asks for a WORKSPACE-scoped token, which is all the managed route can use: its
// `opencode serve` daemon is shared by every session in the workspace, so there is no session
// to scope it to. A named session gets one scoped to that session, which the tmux route can
// use because a session is its own process there.
//
// Empty when the deployment runs no engines, which is the normal case.
//
// It rides the environment rather than the config file because the file is shared and, on the
// tmux route, `new-session -e` keeps the value out of /proc/*/cmdline and pane_start_command
// (the same rule every provider key here follows).
var EngineEnv = func(session string) []string { return nil }
