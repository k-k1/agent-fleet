package main

import (
	"context"
	"encoding/json"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
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

// lcppModels reads the chat-role engine's own declared models straight off the catalog
// (engines.go's engineCatalogRows) — the same "llm" catalog key lcpp/driver.go's runTurn
// always resolves a token against. A deployment with no engines, or none of them
// chat-capable, returns nil (the caller's "empty is a valid answer" path).
//
// A member's own connection (docs/log/107) is checked FIRST: when set, the launch menu is
// built from THAT connection's GET {base}/v1/models, never the deployment's catalogue. This
// must never block the launch menu — lcppMemberFetchModelsCached's own client carries a 3s
// timeout, and a box that does not answer in time returns nil here exactly like "no models",
// not an error (the opencode 10s-timeout lesson, docs/log/54).
func lcppModels(ctx context.Context) []agents.ModelChoice {
	if conn, ok := harnessMemberConn(); ok {
		models := lcppMemberFetchModelsCached(ctx, conn)
		list := make([]agents.ModelChoice, 0, len(models))
		for _, m := range models {
			list = append(list, agents.ModelChoice{ID: m.ID, Label: m.ID})
		}
		return list
	}
	for _, e := range engineCatalogRows(ctx) {
		if e.Key != "llm" || e.api() != engineAPIChat {
			continue
		}
		labels := engineModelLabels(e)
		list := make([]agents.ModelChoice, 0, len(e.Models))
		for _, id := range e.Models {
			mc := agents.ModelChoice{ID: id, Label: id}
			if l, ok := labels[id]; ok {
				mc.Label = l
			}
			list = append(list, mc)
		}
		return list
	}
	return nil
}

func handleAgentModels(w http.ResponseWriter, r *http.Request) {
	// The Console asks with its CURRENT settings (?hidden=… and, for opencode, ?catalog=…),
	// which its debounced save may not have delivered here yet; the answer is computed for them,
	// so it is exactly the answer for what that screen shows (#972 review, rounds 3–5). Without
	// them — MCP list_models, an older Console — the saved ui-prefs apply, as before.
	hiddenRaw, hiddenGiven := requestHiddenModels(r)
	// claude's registered models travel the same way (?custom=): right after a member registers
	// one, the fail-safe and the "fall back to a registered model" recommendation must count it.
	// Either setting given makes the whole answer explicit, the other read from ui-prefs.
	claudeCustom, customGiven := requestClaudeCustomModels(r)
	if !customGiven {
		claudeCustom = uiprefs.ClaudeCustomModels()
	}
	if customGiven && !hiddenGiven {
		hiddenRaw, hiddenGiven = sessionx.HiddenModelsRaw(r.PathValue("kind")), true
	}
	catalogPref := uiprefs.OpencodeCatalog()
	if r.URL.Query().Has("catalog") {
		catalogPref = opencode.CatalogPref(r.URL.Query().Get("catalog"))
	}
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
		for _, id := range claudeCustom {
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
		list, route = opencode.CatalogWithRoute(ids, catalogPref)
	case "agy":
		list = agy.Models()
	case "copilot":
		// PTY scrape of the TUI /model picker (live, so it reflects the plan —
		// docs/log/36 addendum; Free offers only Auto, i.e. an empty list).
		// Unspecified means auto routing.
		list = copilot.Models()
	case "muse":
		// `model/list` over the session protocol (ADR 0095 decision 10): muse has no `models`
		// subcommand, so the catalog is a wire query against a host. Empty until the binary is
		// installed AND a credential is stored — the catalog is the authenticated account's.
		list = muse.Models()
	case "lcpp":
		// The engine catalog IS the model list (ADR 0093 decision 7): there is no vendor
		// CLI account to ask, so this reads the same chat-role catalog row driver.go's
		// runTurn resolves an engine token against (its fixed engineKey="llm"). Empty when
		// the deployment has no engines, or none of them are chat-capable — same "nothing
		// to offer" shape as every other kind.
		list = lcppModels(r.Context())
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
	if hiddenGiven {
		list = sessionx.FilterVisibleModelsIn(sessionx.EffectiveHiddenWith(r.PathValue("kind"), hiddenRaw, claudeCustom), list)
	} else {
		list = sessionx.FilterVisibleModels(r.PathValue("kind"), list)
	}
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
	// What "recommended" resolves to on this kind, per tier (Issue #972) — the Agent's own
	// answer, so the Console's "推奨（現在: X）" names the model that actually runs instead of
	// re-deriving it. Only the kinds the assistant and AI assist can run; the rest have no
	// "recommended" choice to explain.
	if _, ok := chatx.ChatProviders[r.PathValue("kind")]; ok {
		if hiddenGiven {
			out["recommended"] = chatx.RecommendedModelsWithHidden(r.PathValue("kind"), hiddenRaw, claudeCustom)
		} else {
			out["recommended"] = chatx.RecommendedModels(r.PathValue("kind"))
		}
	}
	if route != "" {
		out["route"] = route
	}
	if reason := emptyReason(enumerated, offered, len(list)); reason != "" {
		out["reason"] = reason
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// requestHiddenModels reads ?hidden= — the Console's hiddenModels[kind] as a JSON array of
// strings. ok=false when absent or malformed — not an array (`null` included: json.Unmarshal
// accepts it into a nil slice), or an element that is not a string — and the saved ui-prefs
// apply instead (#972 review, round 6). Blank strings are skipped, as the Console skips them.
func requestHiddenModels(r *http.Request) (raw []string, ok bool) {
	q := r.URL.Query()
	if !q.Has("hidden") {
		return nil, false
	}
	var vals []any
	if json.Unmarshal([]byte(q.Get("hidden")), &vals) != nil || vals == nil {
		return nil, false
	}
	raw = []string{}
	for _, v := range vals {
		s, isString := v.(string)
		if !isString {
			return nil, false
		}
		if strings.TrimSpace(s) != "" {
			raw = append(raw, s)
		}
	}
	return raw, true
}

// requestClaudeCustomModels reads ?custom= — the Console's claudeCustomModels as a JSON array,
// normalized by the same rule as the saved list. ok=false when absent or not a JSON array, and
// the saved ui-prefs apply instead.
func requestClaudeCustomModels(r *http.Request) (ids []string, ok bool) {
	q := r.URL.Query()
	if !q.Has("custom") {
		return nil, false
	}
	var vals []any
	if json.Unmarshal([]byte(q.Get("custom")), &vals) != nil || vals == nil {
		return nil, false
	}
	return uiprefs.NormalizeClaudeCustomModels(vals), true
}
