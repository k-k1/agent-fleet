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
// errors; the launch picker then just offers the default entry.
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
			Slug                      string          `json:"slug"`
			DisplayName               string          `json:"display_name"`
			Visibility                string          `json:"visibility"`
			Priority                  int             `json:"priority"`
			DefaultReasoningEffort    string          `json:"default_reasoning_effort"`
			DefaultReasoningLevel     string          `json:"default_reasoning_level"`
			SupportedReasoningEfforts json.RawMessage `json:"supported_reasoning_efforts"`
			SupportedReasoningLevels  json.RawMessage `json:"supported_reasoning_levels"`
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
		efforts := parseEffortList(m.SupportedReasoningEfforts)
		if len(efforts) == 0 {
			efforts = parseEffortList(m.SupportedReasoningLevels)
		}
		def := m.DefaultReasoningEffort
		if def == "" {
			def = m.DefaultReasoningLevel
		}
		rows = append(rows, row{agents.ModelChoice{
			ID: m.Slug, Label: label, Efforts: efforts, DefaultEffort: def,
		}, m.Priority})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].prio < rows[j].prio })
	list := make([]agents.ModelChoice, len(rows))
	for i, r := range rows {
		list[i] = r.ModelChoice
	}
	return list, nil
}

// parseEffortList accepts both catalog shapes seen across Codex releases:
// ["low","medium"] and [{"effort":"low"}, ...]. Unknown shapes degrade to
// no metadata; the Console can still offer a small compatibility fallback.
func parseEffortList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var strs []string
	if json.Unmarshal(raw, &strs) == nil {
		return compactStrings(strs)
	}
	var rows []struct {
		Effort string `json:"effort"`
		Level  string `json:"level"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	vals := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Effort != "" {
			vals = append(vals, r.Effort)
		} else {
			vals = append(vals, r.Level)
		}
	}
	return compactStrings(vals)
}

func compactStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
