package codex

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Models enumerates the models the codex TUI's /model picker offers, via
// `codex debug models` — the raw catalog codex sees, refreshed from OpenAI's
// models endpoint with codex's own (ChatGPT subscription) auth, so deprecations
// and new models track the server without a Console release. The OpenAI platform
// API's GET /v1/models is NOT used on purpose: it needs a separate API key the
// fleet doesn't hold, and lists the whole API zoo (embeddings, audio, …) instead
// of this ChatGPT-gated agentic catalog. Returns nil when the CLI is absent or
// errors; the launch picker then just offers 既定.
//
// Cached briefly: the Console fetches on every launch-modal open, and the CLI
// refresh costs ~2s. Failures are not cached (stale-if-error below).
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
	out, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err != nil {
		return modelsList // stale-if-error: an expired cache still beats an empty picker
	}
	list, err := parseCatalog(out)
	if err != nil {
		return modelsList
	}
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// parseCatalog extracts the user-selectable models from a `codex debug models`
// dump: visibility "list" is exactly the /model picker's population (internal
// entries like codex-auto-review carry "hide"), ordered by ascending priority
// (the picker's order).
func parseCatalog(b []byte) ([]agents.ModelChoice, error) {
	var doc struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Priority    int    `json:"priority"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	type row struct {
		agents.ModelChoice
		prio int
	}
	var rows []row
	for _, m := range doc.Models {
		if m.Visibility != "list" || m.Slug == "" {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		rows = append(rows, row{agents.ModelChoice{ID: m.Slug, Label: label}, m.Priority})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].prio < rows[j].prio })
	list := make([]agents.ModelChoice, len(rows))
	for i, r := range rows {
		list[i] = r.ModelChoice
	}
	return list, nil
}
