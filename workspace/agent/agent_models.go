package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// handleAgentModels (GET /agents/{kind}/models) returns the launch-time model
// choices per kind:
//   - claude: fixed tier aliases (claude.Models) — no live catalog exists; launch
//     takes `--model <alias>` and the alias tracks its tier's newest model. The
//     Console picker keeps its own copy (settings.ts CLAUDE_MODELS); this serves
//     the MCP list_models so assistants resolve claude ids the same way as the
//     other kinds.
//   - codex: `codex debug models` — the /model picker's catalog, refreshed from
//     OpenAI's models endpoint with codex's own subscription auth (id + display name)
//   - opencode: `opencode models` — reflects the user's connected providers (ids only)
//   - agy: `agy models` — display names, accepted verbatim by `agy --model`
//
// An empty list is a valid answer (CLI absent / offline) — the Console picker then
// offers only the default entry.
//
// The order is whatever the upstream of each kind recommends, passed through as is
// (codex's priority order; the enumeration order of cursor / kiro / copilot / agy means
// "newest first, grouped by family"). Only opencode is normalized in the package itself
// (catalog.go), because it has two fetch paths and no single upstream order. The policy
// is explained in agents/modelsort.go.
// emptyReason names the step that emptied the menu, "" when there is one to show.
//
// Three causes render identically in the picker ("only the default model is available —
// check the connection and the plan"), and telling them apart took reading the Agent's log,
// which a member cannot do. The answer is the OUTERMOST step that was already empty, because
// that is the one to act on: nothing hid the models if there were none to begin with.
//
//	catalog_empty — the CLI/daemon answered with nothing. Not always a fault: an account with
//	                no plan, a kind whose enumeration is legitimately empty (Copilot Free
//	                offers Auto alone), a provider that cannot be reached, and an enumeration
//	                killed by its own timeout all land here. It is still worth saying,
//	                because it rules the other two out.
//	route         — opencode only: the catalog had ids and the selected billing route dropped
//	                every one (off, or own with no provider of the user's own).
//	hidden        — what survived is excluded in settings (hiddenModels).
//
// enumerated is -1 for the kinds that do no shaping of their own, so for them the first case
// reads "the kind itself offered nothing".
func emptyReason(enumerated, offered, final int) string {
	if final > 0 {
		return "" // a reason for a menu that works is noise
	}
	switch {
	case enumerated == 0 || (enumerated < 0 && offered == 0):
		return "catalog_empty"
	case offered == 0:
		return "route"
	default:
		return "hidden"
	}
}

func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var list []agents.ModelChoice
	// route is the opencode billing route the list was actually shaped by — the selected one
	// unless Catalog's empty-menu rescue had to ignore it. Empty for every other kind.
	route := ""
	// enumerated is what the CLI/daemon answered before opencode's billing-route shaping,
	// so "the route hid everything" can be told from "there was nothing to hide". -1 for
	// every other kind, which does no shaping of its own.
	enumerated := -1
	switch r.PathValue("kind") {
	case "claude":
		list = claude.Models()
		seen := make(map[string]bool, len(list))
		for _, model := range list {
			seen[strings.ToLower(model.ID)] = true
		}
		for _, id := range uiprefs.ClaudeCustomModels() {
			if key := strings.ToLower(id); !seen[key] {
				list = append(list, agents.ModelChoice{ID: id, Label: id})
				seen[key] = true
			}
		}
	case "codex":
		list = codex.Models()
	case "cursor":
		// Line-parsed from `cursor-agent models` (id - display name, tied to the
		// account — docs/log/40).
		list = cursor.Models()
	case "kiro":
		// `kiro-cli chat --list-models -f json` (fully machine-readable, tied to the
		// account — docs/log/43).
		list = kiro.Models()
	case "opencode":
		// Shaping of the list only (catalog.go): one key can open both the Zen
		// (pay-as-you-go) and Go (subscription) providers, so the same model name
		// appears under both. Whether Zen is shown follows the user setting (ui-prefs
		// opencodeCatalog), and the order is normalized to Go first then id ascending
		// (the upstream order differs between the daemon and the CLI path). An
		// explicit model is never swallowed here: handleCreateSession validates it
		// against the full, unshaped catalog.
		//
		// The shaping is reported alongside the list: when the selected route yields
		// nothing the rescue quietly re-shapes with Zen, and the Console has to be able to
		// say so rather than keep claiming the route the user chose (docs/log/103).
		ids := opencode.Models()
		enumerated = len(ids)
		list, route = opencode.CatalogWithRoute(ids, uiprefs.OpencodeCatalog())
	case "agy":
		list = agy.Models()
	case "copilot":
		// PTY scrape of the TUI /model picker (live, so it reflects the plan —
		// docs/log/36 addendum; Free offers only Auto, i.e. an empty list).
		// Unspecified means auto routing.
		list = copilot.Models()
	default:
		httpx.WriteErr(w, http.StatusNotFound, "unknown_kind", "no model catalog for this kind")
		return
	}
	// How many the kind itself offered, before this handler narrows it. An empty menu has
	// several causes that look identical on screen, and the Console has never been able to
	// tell them apart ("only the default model is available — check the connection and the
	// plan" is a guess it prints for all of them). Counting the two narrowing steps is
	// enough to name which one emptied it; see `reason` below.
	offered := len(list)
	// Drop the models the user hides (ui-prefs hiddenModels) last. This is where the
	// Console picker and the MCP list_models meet, so one place covers both (the same
	// shape as opencodeCatalog). An explicitly named hidden model is refused separately
	// by the guard in handleCreateSession.
	list = sessionx.FilterVisibleModels(r.PathValue("kind"), list)
	if list == nil {
		list = []agents.ModelChoice{}
	}
	// Who made each model, for the picker's brand marks (model_provider.go). Filled here
	// rather than in each kind's package: the answer comes from one catalog and one table, and
	// a kind's own list carries billing routes, not makers. Best-effort — an id that cannot be
	// placed keeps Provider empty and simply gets no mark.
	for i := range list {
		list[i].Provider = resolveModelProvider(r.PathValue("kind"), list[i].ID)
	}
	out := map[string]any{"models": list}
	if route != "" {
		out["route"] = route
	}
	if reason := emptyReason(enumerated, offered, len(list)); reason != "" {
		out["reason"] = reason
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
