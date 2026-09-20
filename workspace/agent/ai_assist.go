package main

// GET /ai-assist/resolution (docs/log/103 §103.6/§103.7): for each of the 8 features that go
// through OneShotHeadless, whether it is on and which backend it would currently run on.
//
// The endpoint MUST NOT start a CLI, ever — not "rarely", not "only when cold" (docs/log/103
// §103.8-3, docs/log/103-review 重大4). The naive form of this answer needs two expensive
// things: an auth-status check per backend (headlessAgentAvailable, 1-minute cache, a real
// `claude auth status`/`codex login status`/… call on a miss) and a model catalog fetch for
// whichever backend answers (codex.Models() 15s / opencode.Models() 10s / agy.Models() 15s).
// Both are cut, not merely cached harder:
//   - kind comes from chatx.ResolveOneShotCached, which peeks the SAME availability cache
//     oneShotKind reads but never falls through to the exec call on a miss — a cold entry
//     answers source:"unknown" instead of paying to find out.
//   - the model NAME is not answered here at all. The Console already fetches
//     /agents/{kind}/models to draw §1's model rows (useModelOptions(kind) in
//     lib/agentModels.ts), so it draws THIS row's label from that same catalog — the extra
//     round trip this endpoint would otherwise need costs nothing new.
import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// aiAssistFeature is one row of the catalog behind both this endpoint and the enforcement
// gates each feature's own handler carries (chat_plan.go, chat_suggest_reply.go,
// session_suggest_reply.go, chat_title.go, session_title.go, fs_suggest_edit.go,
// session_translate.go). Kept here, not derived from usagex's feature constants, because
// "enabled" and "tier" are AI-assist-specific facts the ledger has no reason to know.
type aiAssistFeature struct {
	id      string
	tier    chatx.OneShotTier
	enabled func() bool
}

// aiAssistFeatures is the 8 features that go through OneShotHeadless (docs/log/103 §103.2) —
// the same 8 the ledger already records as separate `feature` values (usagex/ledger.go). The
// order here is display order (§103.7's card list), not significant otherwise.
var aiAssistFeatures = []aiAssistFeature{
	{usagex.FeatureTitleSession, chatx.OneShotShort, uiprefs.AutoTitleSuggest},
	{usagex.FeatureTitleChat, chatx.OneShotShort, uiprefs.AssistantTitleSuggest},
	{usagex.FeatureBranchSuggest, chatx.OneShotShort, uiprefs.BranchSuggest},
	{usagex.FeatureSuggestSession, chatx.OneShotShort, sessionx.ReplySuggestEnabled},
	{usagex.FeatureSuggestChat, chatx.OneShotShort, uiprefs.ChatReplySuggest},
	{usagex.FeatureSuggestEdit, chatx.OneShotProse, uiprefs.EditSuggest},
	{usagex.FeaturePlanUpdate, chatx.OneShotProse, uiprefs.PlanUpdate},
	{usagex.FeatureTranslate, chatx.OneShotProse, uiprefs.MirrorTranslate},
}

// aiAssistResolutionRow is one feature's answer. Model is deliberately absent — see the file
// header. Source is "pin" / "default" / "unknown" (chatx.OneShotSourcePin/Default, or
// "unknown" when the availability cache cannot answer yet without starting a CLI).
type aiAssistResolutionRow struct {
	Feature string `json:"feature"`
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind,omitempty"`
	Source  string `json:"source"`
}

func handleAIAssistResolution(w http.ResponseWriter, r *http.Request) {
	rows := make([]aiAssistResolutionRow, 0, len(aiAssistFeatures))
	for _, f := range aiAssistFeatures {
		row := aiAssistResolutionRow{Feature: f.id, Enabled: f.enabled(), Source: "unknown"}
		if kind, source, ok := chatx.ResolveOneShotCached(f.id); ok {
			row.Kind, row.Source = kind, source
		}
		rows = append(rows, row)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"features": rows})
}
