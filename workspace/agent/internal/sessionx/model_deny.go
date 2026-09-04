package sessionx

// Hidden models (ui-prefs hiddenModels, Settings > Agents > each card > behaviour settings).
//
// The motive is preventing billing accidents: on Claude's Team plan Fable is billed as API
// credit, and while it sits in the picker both "picked it by mistake" and "the assistant picked
// it out of list_models" can happen. So a deny list is kept per kind and enforced in two stages:
//
//   (1) drop it from the catalog — handleAgentModels, where the Console picker and MCP
//       list_models meet
//   (2) refuse the launch — create_session / session settings change / the assistant's model
//       resolution
//
// Stage 1 alone lets every path that writes an id explicitly through (the model field of a
// scheduled run, user-defined assistants, a direct MCP argument), so stage 2 is the guard and
// stage 1 is only the signpost.
//
// This prevents accidental selection; it is not a billing guard. Typing /model inside the TUI,
// or changing the model through the CLI's own settings, is not blocked (the policy is to leave
// the agent's internal state alone). A real per-organisation ban belongs in the plan provider's
// own settings.
//
// The deny list is scoped per kind, because model id namespaces differ per kind (fable is
// claude, gpt-5.6-… is codex) and the same string can mean a different thing under another kind.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// normModelToken folds an id or alias into the normal form used for matching. Separators
// (/ . _ space) become hyphens and the result is lowercased, so an "alias inside full id"
// relation such as "fable" within "claude-fable-5" can be decided on token boundaries.
func normModelToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("/", "-", "_", "-", ".", "-", " ", "-", ":", "-").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// ModelMatchesHidden reports whether a requested model hits a deny-list entry.
//
// The base case is exact equality. On top of that, hiding an alias must also hide every full id
// that contains it: claude accepts either the alias (fable) or the full id (claude-fable-5) on
// --model, so denying the alias alone would be bypassed by naming the full id.
//
// This implied match applies only when the deny entry is a single token. Extending it to a
// concrete catalog id (several tokens, like gpt-5-4) takes out other models that merely have it
// as a prefix — measured: hiding GPT-5.4 removed gpt-5-4-mini as well. A single token names a
// family (fable / opus / sonnet / haiku); several tokens name one concrete model.
func ModelMatchesHidden(requested, hidden string) bool {
	r, h := normModelToken(requested), normModelToken(hidden)
	if r == "" || h == "" {
		return false
	}
	if r == h {
		return true
	}
	if strings.Contains(h, "-") {
		return false // only a concrete id was hidden; another model sharing the prefix is not it
	}
	return strings.Contains("-"+r+"-", "-"+h+"-")
}

// HiddenModelsFor returns the effective deny list for a kind. ui-prefs is opaque JSON owned by
// the Console, so a wrong type or broken content falls back to "nothing hidden".
//
// Only claude has a failsafe: denying all four fixed tiers would leave no model to launch with,
// because the claude picker has no "default" choice (a deliberate design in settings.ts). A
// broken setting that hides everything is ignored and the plain catalog comes back. Kinds with
// a live catalog need no such protection — for them "empty catalog = launch on the default" is
// already a normal state.
func HiddenModelsFor(kind string) []string {
	raw, ok := uiprefs.Read()["hiddenModels"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := raw[kind].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if kind == "claude" {
		all := true
		for _, c := range claude.Models() {
			if !modelHiddenIn(out, c.ID) {
				all = false
				break
			}
		}
		if all {
			return nil
		}
	}
	return out
}

// modelHiddenIn decides against an already-resolved deny list; split out so HiddenModelsFor is
// not read again for every candidate.
func modelHiddenIn(hidden []string, requested string) bool {
	for _, h := range hidden {
		if ModelMatchesHidden(requested, h) {
			return true
		}
	}
	return false
}

// ModelHidden reports whether requested is denied by this kind's settings. The empty string
// (= defer to the CLI's default) is always allowed: nothing was chosen, so there is nothing to
// block.
func ModelHidden(kind, requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return false
	}
	return modelHiddenIn(HiddenModelsFor(kind), requested)
}

// hiddenModelError is the wording the launch guard returns. Both the Console user and the
// assistant (an LLM) read it, so it names the cause (denied in settings) and the way out (pick
// another model, or clear the deny entry).
func hiddenModelError(requested string) string {
	return "モデル " + requested + " は設定「使わないモデル」で除外されています。" +
		"別のモデルを指定するか、設定 > エージェント > 動作設定 で除外を解除してください。"
}

// FilterVisibleModels drops denied models from a catalog. It runs via handleAgentModels, so the
// Console picker and MCP list_models show the same result.
func FilterVisibleModels(kind string, list []agents.ModelChoice) []agents.ModelChoice {
	hidden := HiddenModelsFor(kind)
	if len(hidden) == 0 {
		return list
	}
	out := make([]agents.ModelChoice, 0, len(list))
	for _, c := range list {
		if modelHiddenIn(hidden, c.ID) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// VisibleModel passes a fixed recommendation (the assistant's "sonnet" / "haiku", agy's default
// name) through the deny settings. Returns "" when denied, and the caller then defers to the
// CLI's own default.
func VisibleModel(kind, model string) string {
	if ModelHidden(kind, model) {
		return ""
	}
	return model
}

// VisibleModelIDs is for id-only catalogs (opencode.Models() and the like). The assistant's
// automatic "recommended model" choice goes through here, so a denied model is never picked
// automatically.
func VisibleModelIDs(kind string, ids []string) []string {
	hidden := HiddenModelsFor(kind)
	if len(hidden) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if modelHiddenIn(hidden, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
