package kiro

// Launch-time model catalog, fetched live against the account (docs/log/43 §2.6).
// `kiro-cli chat --list-models -f json` returns fully machine-readable JSON, so no line
// scraping like cursor's is needed. `auto` (the default: 1M ctx, passed as no flag) is
// kept out of the catalog. Measured: a named model can be selected even on the Free plan,
// so no cursor-style Free narrowing is needed. Cached for 10 minutes, stale-if-error.
// Effort is a separate flag independent of the model (--effort) and is not folded in here;
// program.go passes m.Effort through.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const modelsTTL = 10 * time.Minute

// kiroDefaultWindow is the fallback context window (tokens) used when the catalog
// hasn't been fetched or the running model id isn't in it. It only affects the token
// COUNT shown next to kiro's live context %; the % itself round-trips exactly because
// the window is passed explicitly to the ContextBar (see context.go / session_usage.go).
const kiroDefaultWindow = 200_000

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = never fetched, or the fetch failed
var modelWindows map[string]int     // model_id -> context_window_tokens (auto included; nil = never fetched)

// listModelsOut is the shape of `kiro-cli chat --list-models -f json` (measured on 2.14.1).
type listModelsOut struct {
	Models []struct {
		ModelName           string `json:"model_name"`
		Description         string `json:"description"`
		ModelID             string `json:"model_id"`
		ContextWindowTokens int    `json:"context_window_tokens"`
	} `json:"models"`
	DefaultModel string `json:"default_model"`
}

// Models returns the account's selectable launch models (empty means the picker offers
// only the default, [auto]).
func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	return ensureCatalogLocked()
}

// ModelWindow returns the context-window token count for a model id (incl "auto"), from
// the cached `--list-models` catalog (Track D, for the pct-to-token conversion). Never
// fetched or unknown gives 0.
func ModelWindow(id string) int {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	ensureCatalogLocked()
	return modelWindows[id]
}

// ensureCatalogLocked refreshes the model list + window map when stale, returning the
// current list (stale-if-error). Caller holds modelsMu.
func ensureCatalogLocked() []agents.ModelChoice {
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, windows, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error; windows keeps its previous value too
	}
	modelsList, modelWindows, modelsAt = list, windows, time.Now()
	return modelsList
}

func probeModels() ([]agents.ModelChoice, map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "chat", "--list-models", "-f", "json").Output()
	if err != nil {
		return nil, nil, err
	}
	list, windows := parseModels(out)
	return list, windows, nil
}

// parseModels extracts the catalog from the --list-models JSON. The picker list drops
// `auto` (the default, i.e. no flag); the window map keeps EVERY model incl auto (the
// pct-to-token conversion needs auto's 1M window too). Label is the description, which
// reads better, falling back to model_name.
func parseModels(b []byte) ([]agents.ModelChoice, map[string]int) {
	windows := map[string]int{}
	var lm listModelsOut
	if json.Unmarshal(b, &lm) != nil {
		return []agents.ModelChoice{}, windows // non-nil empty: on output drift, fall back safely to the default only
	}
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, m := range lm.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		if m.ContextWindowTokens > 0 {
			windows[id] = m.ContextWindowTokens
		}
		if id == "auto" || seen[id] {
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(m.Description)
		if label == "" {
			label = m.ModelName
		}
		if label == "" {
			label = id
		}
		list = append(list, agents.ModelChoice{ID: id, Label: label})
	}
	if list == nil {
		list = []agents.ModelChoice{}
	}
	return list, windows
}
