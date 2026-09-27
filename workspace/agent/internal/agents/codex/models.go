package codex

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
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
var modelsRetiring map[string]agents.ModelRetiring

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
	modelsRetiring = parseRetiring(out)
	modelsAt = time.Now()
	return modelsList
}

// Retiring reports whether the catalog carries an `upgrade` for id — codex's own notice that
// the model is being retired ("GPT-5.5 retires on October 14, 2026. Switch to GPT-5.6 Sol").
// It stays in the picker (a member may still choose it until then), but is never what AF
// recommends (Issue #972). Answers from the last Models() read; false before the first one.
func Retiring(id string) bool {
	_, ok := Retirement(id)
	return ok
}

// Retirement is Retiring plus what the notice says, for the picker's "retiring" mark (Issue
// #1021). The fields are empty where the notice does not carry them.
func Retirement(id string) (agents.ModelRetiring, bool) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	r, ok := modelsRetiring[id]
	return r, ok
}

// parseRetiring collects the slugs whose catalog entry names an upgrade target. Kept apart
// from parseCatalog so the list stays the picker's population and the notice is attached only
// where a caller asks for it.
func parseRetiring(b []byte) map[string]agents.ModelRetiring {
	var doc struct {
		Models []struct {
			Slug    string          `json:"slug"`
			Upgrade json.RawMessage `json:"upgrade"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	out := map[string]agents.ModelRetiring{}
	for _, m := range doc.Models {
		u := strings.TrimSpace(string(m.Upgrade))
		if m.Slug == "" || u == "" || u == "null" {
			continue
		}
		// An upgrade of another shape still marks the model retiring; it just says nothing more.
		var up struct {
			Model     string `json:"model"`
			Note      string `json:"migration_markdown"`
			RetiresAt string `json:"retirement_at"`
		}
		_ = json.Unmarshal(m.Upgrade, &up)
		out[m.Slug] = agents.ModelRetiring{
			At: strings.TrimSpace(up.RetiresAt), Note: strings.TrimSpace(up.Note), Successor: strings.TrimSpace(up.Model),
		}
	}
	return out
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
