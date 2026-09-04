package codex

// Fleet-side wiring that enables codex memories (docs/log/39 P4, resolution #4).
//
// codex's memories feature flag is stable but off by default, and turning it on makes the
// per-rollout extraction (phase 1) and the internal sub-agent's global consolidation (phase 2)
// run in the background and spend tokens. That cost is why resolution #4 pushed enabling it to
// P4, so this file is not just a toggle: it also lays down defaults that hold the spend down at
// the moment it is enabled.
//
// Enabling means `features.memories = true` in ~/.codex/config.toml, equivalent to
// `codex features enable memories`. Measured on codex 0.145.0: the value we write is read back
// by `codex features list` as `memories stable true`. Editing the TOML ourselves instead of
// invoking the CLI is done for the same reason as rate_limit_model_nudge — to preserve the
// user's comments and [projects.*] trust settings byte for byte, and to be able to write the
// setting without depending on the presence or version of the codex binary.
//
// Enabling does not immediately create ~/.codex/memories/; codex creates it the next time a
// codex session runs. The root declaration in docs/log/39 is RequireDir, so until then it does
// not appear as a memory root, and the UI shows this "enabled but not materialized" state
// distinctly.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MemoriesDir is where codex creates its memories workspace — the same path as the root
// declaration in docs/log/39. If the two drift apart the result is "enabled, but the root never
// appears", so the path is defined in one place.
func MemoriesDir() string {
	return filepath.Join(paths.HomeDir(), ".codex", "memories")
}

// MemoriesEnabled reports whether config.toml turns the memories feature on. codex defaults to
// off, so a missing key means disabled.
func MemoriesEnabled() bool {
	b, _ := os.ReadFile(codexConfigPath())
	return memoriesEnabled(b)
}

func memoriesEnabled(b []byte) bool {
	v, found := tomlBool(b, "features", "memories")
	return found && v
}

// MemoriesMaterialized reports whether codex has actually created the workspace, i.e. whether a
// memory root can appear. It is false right after enabling and becomes true once the next codex
// session has run.
func MemoriesMaterialized() bool {
	st, err := os.Stat(MemoriesDir())
	return err == nil && st.IsDir()
}

// setMemories rewrites features.memories. Only when enabling, and only when no [memories] table
// exists yet, it also seeds conservative tuning. Disabling touches nothing, so that a round trip
// through the toggle does not erase values the user has tuned.
func setMemories(b []byte, on bool) []byte {
	b = tomlSetBool(b, "features", "memories", on)
	if on {
		b = seedMemoriesTuning(b)
	}
	return b
}

// seedMemoriesTuning writes conservative defaults, and only when enabling. If [memories] already
// exists it changes nothing, so a toggle never overwrites values the user (or a future us) has
// tuned.
//
// Every deviation goes in the direction of running less. codex's defaults extract after six
// hours of rollout idle time and run eight in parallel per startup, which is heavy here, where
// several workspaces share one host and would all run right after startup. Users can override
// any of it in config.toml.
//
// Measured: codex 0.145.0's `app-server --strict-config` accepts all of these key names. In the
// same run a non-existent key is rejected with "unknown configuration field", so acceptance
// proves the spelling is real. Production codex launches without --strict-config, meaning
// unknown keys are silently ignored — if upstream renames these, the seed stops taking effect
// with no sign of it. TestDriftCodexMemoriesTuningKeysValid in drift_test.go is what catches
// that in CI.
func seedMemoriesTuning(b []byte) []byte {
	if tomlHasSection(b, "memories") {
		return b
	}
	lines := []string{
		"[memories]",
		"# agent-fleet が memories 有効化時に置いた保守的な既定（docs/log/39 P4）。自由に調整可。",
	}
	for _, kv := range memoriesTuning() {
		lines = append(lines, kv[0]+" = "+kv[1])
	}
	return append(b, []byte(tomlAppendPrefix(b)+strings.Join(lines, "\n")+"\n")...)
}

// memoriesTuning is the table of {key, TOML value} pairs to seed. seedMemoriesTuning renders it,
// and the drift test builds -c memories.<key>=<value> from it to run against the real binary:
// hand-copying the expected values would only make the tests agree with each other
// (drift_test.go's contract).
func memoriesTuning() [][2]string {
	out := [][2]string{
		{"min_rollout_idle_hours", "12"},
		{"max_rollouts_per_startup", "4"},
	}
	if m := cheapModelFn(); m != "" {
		out = append(out,
			[2]string{"extract_model", strconv.Quote(m)},
			[2]string{"consolidation_model", strconv.Quote(m)})
	}
	return out
}

// cheapModelFn is the substitution point; unit tests do not launch a real codex.
var cheapModelFn = cheapModel

// cheapModel picks the cheap model to spend on extraction/consolidation out of the catalog as
// it stands at that moment. No slug is baked in as a constant, because codex's model catalog is
// swapped out server-side and a stale pin becomes a non-existent model that breaks the whole
// pipeline (model selection at launch reads the dynamic catalog for the same reason). With no
// match it writes nothing and defers to codex's default: disabling itself is safer than leaving
// a pin that no longer resolves.
func cheapModel() string {
	list := Models()
	for _, want := range []string{"nano", "mini"} {
		for _, m := range list {
			if strings.Contains(strings.ToLower(m.ID), want) {
				return m.ID
			}
		}
	}
	return ""
}
