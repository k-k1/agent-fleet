package cursor

// Launch-time model catalog, fetched live and tied to the account (docs/log/40 decision
// 6). cursor has an official `cursor-agent models` (no TUI scraping as for copilot) that
// returns `id - display name` rows (measured on v2026.07.20). Effort is folded into the
// model id itself (gpt-5.3-codex-high, claude-opus-4-8-thinking-high), so no separate
// Efforts are attached. `auto` (the default, meaning no flag) is dropped from the
// catalog. Cached for 10 minutes, stale-if-error.
//
// Free-plan narrowing (docs/log/40 §Free, measured in session2): `cursor-agent models`
// lists every model regardless of plan, but a Free plan cannot use a named model at all
// (measured: `ActionRequiredError: Named models unavailable Free plans can only use
// Auto.`). Only Auto (the picker's default, already excluded from the catalog) and the
// Composer family work (measured: composer-2.5 returns result:"ok"). So on a Free plan
// (decided from `Subscription Tier` in `cursor-agent about`) the catalog is narrowed to
// the composer family, hiding named models that would hit a wall when selected — the fix
// for a user picking GLM-5.2 and getting an upgrade demand. On a paid plan, or when the
// plan cannot be determined, the whole catalog stays (never over-restrict).

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const modelsTTL = 10 * time.Minute

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = not fetched yet, or the fetch failed

// Models returns the account's selectable launch models (empty ⇒ picker offers the
// default [auto] only).
func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error
	}
	if freePlan() {
		list = freeUsableModels(list)
	}
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// freeUsableModels keeps only the models a Free plan can actually launch — the
// Composer family (Auto is the picker's own default entry and was never in this list).
// A named model raises `ActionRequiredError` on Free, so it is hidden. Models() does not
// call this on a paid plan.
func freeUsableModels(list []agents.ModelChoice) []agents.ModelChoice {
	out := make([]agents.ModelChoice, 0, len(list))
	for _, m := range list {
		if strings.HasPrefix(m.ID, "composer") {
			out = append(out, m)
		}
	}
	return out
}

// --- Free-plan detection (Subscription Tier from `cursor-agent about`) ------------

var tierMu sync.Mutex
var tierAt time.Time
var tierFree, tierKnown bool

// freePlan reports whether the signed-in account is on the Free plan. Cached with
// the same TTL as the model catalog, so an upgrade takes up to 10 minutes to show. When
// the plan cannot be determined, the last known value stands (false if there is none, so
// nothing is over-restricted).
func freePlan() bool {
	tierMu.Lock()
	defer tierMu.Unlock()
	if tierKnown && time.Since(tierAt) < modelsTTL {
		return tierFree
	}
	free, ok := probeFreePlan()
	if !ok {
		return tierFree // stale-if-error
	}
	tierFree, tierKnown, tierAt = free, true, time.Now()
	return tierFree
}

// aboutTierRe matches the `Subscription Tier   Free` row of `cursor-agent about`
// (columns are space-aligned; measured on v2026.07.20).
var aboutTierRe = regexp.MustCompile(`(?mi)^\s*Subscription Tier\s+(\S+)`)

func probeFreePlan() (free bool, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "about").Output()
	if err != nil {
		return false, false
	}
	m := aboutTierRe.FindStringSubmatch(string(out))
	if m == nil {
		return false, false // format drift: treat as unknown and do not narrow
	}
	return strings.EqualFold(strings.TrimSpace(m[1]), "free"), true
}

// modelRowRe matches one `id - Display Name` catalog row (measured). An id starts with a
// lowercase letter and is drawn from [a-z0-9.-]. Trailing notes such as
// "(current, default)" are stripped off the label.
var modelRowRe = regexp.MustCompile(`^([a-z][a-z0-9.\-]*)\s+-\s+(.+)$`)
var annotationRe = regexp.MustCompile(`\s*\((?:current|default)[^)]*\)\s*$`)

func probeModels() ([]agents.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// --disable-auto-update goes first: models can run for up to 20s and would otherwise
	// meet the background-update trigger that fires 2s after start. It is a root option,
	// so it must precede the subcommand.
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "models").Output()
	if err != nil {
		return nil, err
	}
	return parseModels(string(out)), nil
}

// parseModels extracts the model catalog from `cursor-agent models` output.
func parseModels(s string) []agents.ModelChoice {
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, ln := range strings.Split(s, "\n") {
		m := modelRowRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		id := m[1]
		if id == "auto" || seen[id] { // auto is the default (no flag), so it is not in the catalog
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(annotationRe.ReplaceAllString(m[2], ""))
		if label == "" {
			label = id
		}
		list = append(list, agents.ModelChoice{ID: id, Label: label})
	}
	if list == nil {
		return []agents.ModelChoice{} // non-nil empty: on output drift, fall back to the default only
	}
	return list
}
