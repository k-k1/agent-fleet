package agy

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Models enumerates agy's launch-time model choices via `agy models` — one
// display name per line ("Gemini 3.5 Flash (Medium)", "Claude Sonnet 4.6
// (Thinking)", …). The display name IS the id: `agy --model` accepts it
// verbatim (実機検証 2026-07-20 — the TUI status bar reflects the choice).
// Effort variants are baked into the names (Medium/High/Low), so no separate
// efforts metadata. Returns nil when the CLI is absent, unauthenticated
// ("Please sign in"), or on an unsupported host — the picker then offers 既定.
//
// Cached briefly with stale-if-error, mirroring codex.Models(): the Console
// fetches on every launch-modal open.
var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice

func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < time.Minute {
		return modelsList
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "agy", "models").Output()
	if err != nil {
		return modelsList // stale-if-error: an expired cache still beats an empty picker
	}
	if list := parseModels(out); list != nil {
		modelsList = list
		modelsAt = time.Now()
	}
	return modelsList
}

// parseModels turns the line-per-model output into choices, defensively
// skipping anything that doesn't look like a model name (a future banner or
// error text would otherwise pollute the picker).
func parseModels(b []byte) []agents.ModelChoice {
	var list []agents.ModelChoice
	for _, ln := range strings.Split(string(b), "\n") {
		name := strings.TrimSpace(ln)
		if name == "" || strings.Contains(name, "sign in") {
			continue
		}
		list = append(list, agents.ModelChoice{ID: name, Label: name})
	}
	return list
}
