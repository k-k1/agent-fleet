package agy

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Models enumerates agy's launch-time model choices via `agy models`.
//
// ⚠️ The output shape changed under us, and the two forms have to both keep working
// because the image pins 1.1.17 while `agy update` moves a live workspace past it:
//
//	1.1.17:  Gemini 3.5 Flash (Medium)                       ← display name only
//	1.1.19:  gemini-3.5-flash-low<TAB>Gemini 3.5 Flash (Low) ← id, then display name
//
// On the old form the display name IS the id — `agy --model` accepts it verbatim
// (実機検証 2026-07-20). On the new one it is not, and passing the whole line is what
// the CLI answered with, on a real workspace (docs/70 §70.14.8):
//
//	⚠ model gemini-3.5-flash-low    Gemini 3.5 Flash (Low) is not recognized as a
//	  known model or custom model in settings. Using "Gemini 3.7 Flash (High)" instead.
//
// ⚠️ Note what that failure looked like: **the session started and worked**, on a
// silently different model from the one that was picked. Nothing errored.
//
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
		ln = strings.TrimRight(ln, "\r")
		name := strings.TrimSpace(ln)
		if name == "" || strings.Contains(name, "sign in") {
			continue
		}
		// A TAB means the newer two-column form. Split on the FIRST tab only: the
		// display name is free text and may well grow one of its own.
		if id, label, ok := strings.Cut(ln, "\t"); ok {
			id, label = strings.TrimSpace(id), strings.TrimSpace(label)
			if id != "" && label != "" {
				list = append(list, agents.ModelChoice{ID: id, Label: label})
				continue
			}
		}
		// No tab: the old form, where the display name is the id.
		list = append(list, agents.ModelChoice{ID: name, Label: name})
	}
	return list
}
