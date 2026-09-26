package chatx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// useCatalogs answers the recommendation rules' catalog and price reads for one test.
func useCatalogs(t *testing.T, codexIDs, agyIDs []string, retiring map[string]bool, prices map[string]float64) {
	t.Helper()
	prevCodex, prevRetiring, prevAgy, prevPrices := codexModels, codexRetiring, agyModels, testPrices
	t.Cleanup(func() {
		codexModels, codexRetiring, agyModels, testPrices = prevCodex, prevRetiring, prevAgy, prevPrices
	})
	// nil ids = a catalog that could not be read (nil, as codex.Models returns before its first
	// good read); a non-nil empty slice = a read that listed nothing.
	choices := func(ids []string) func() []agents.ModelChoice {
		return func() []agents.ModelChoice {
			if ids == nil {
				return nil
			}
			out := make([]agents.ModelChoice, len(ids))
			for i, id := range ids {
				out[i] = agents.ModelChoice{ID: id, Label: id}
			}
			return out
		}
	}
	codexModels, agyModels = choices(codexIDs), choices(agyIDs)
	codexRetiring = func(id string) bool { return retiring[id] }
	testPrices = prices
}

// The codex catalog as `codex debug models` listed it on 2026-09-26, with models.dev's list
// prices folded the way modelListPrice folds them (input + output/10).
var codexCatalog0926 = []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"}

var codexPrices0926 = map[string]float64{
	"codex/gpt-6-astra":   10 + 5,
	"codex/gpt-6-sol":     2 + 1,
	"codex/gpt-6-luna":    0.1 + 0.05,
	"codex/gpt-5.6-sol":   4 + 2,
	"codex/gpt-5.6-terra": 2 + 1.2,
	"codex/gpt-5.6-luna":  0.2 + 0.12,
	"codex/gpt-5.5":       5 + 3,
}

// The short tier follows the cheapest priced model, so a new generation that undercuts the old
// one is picked up with no source edit — the case that motivated Issue #972 (gpt-6-luna at half
// gpt-5.6-luna's input price, while the old marker search found no "mini" and picked nothing).
func TestRecommendedShortFollowsCheapestCodexModel(t *testing.T) {
	useCatalogs(t, codexCatalog0926, nil, nil, codexPrices0926)
	if got := recommendedUtilityModel(session.KindCodex); got != "gpt-6-luna" {
		t.Fatalf("codex short = %q, want the cheapest listed model gpt-6-luna", got)
	}
	// Without it the next cheapest wins — the ranking, not a fixed id, decides.
	useCatalogs(t, []string{"gpt-6-sol", "gpt-5.6-luna", "gpt-5.6-terra"}, nil, nil, codexPrices0926)
	if got := recommendedUtilityModel(session.KindCodex); got != "gpt-5.6-luna" {
		t.Fatalf("codex short = %q, want gpt-5.6-luna", got)
	}
}

// A model codex announces it is retiring is never recommended, however cheap it is.
func TestRecommendedShortSkipsRetiringCodexModel(t *testing.T) {
	prices := map[string]float64{"codex/gpt-5.5": 0.01, "codex/gpt-6-sol": 3}
	useCatalogs(t, []string{"gpt-5.5", "gpt-6-sol"}, nil, map[string]bool{"gpt-5.5": true}, prices)
	if got := recommendedUtilityModel(session.KindCodex); got != "gpt-6-sol" {
		t.Fatalf("codex short = %q, want the retiring gpt-5.5 skipped", got)
	}
}

// No prices at all (no catalog, a closed network) is exactly the pre-#972 behaviour: the
// size-marker search, and "" (no -m) when it finds nothing.
func TestRecommendedShortWithoutPricesKeepsMarkerSearch(t *testing.T) {
	useCatalogs(t, []string{"gpt-5.6-sol", "gpt-5.4-mini"}, nil, nil, map[string]float64{})
	if got := recommendedUtilityModel(session.KindCodex); got != "gpt-5.4-mini" {
		t.Fatalf("codex short without prices = %q, want the marker pick", got)
	}
	useCatalogs(t, codexCatalog0926, nil, nil, map[string]float64{})
	if got := recommendedUtilityModel(session.KindCodex); got != "" {
		t.Fatalf("codex short without prices or markers = %q, want no -m", got)
	}
}

// agy lists one model once per reasoning effort at one price: the tie goes to the lowest
// effort. With no price for any row, the fixed Flash default stays.
func TestRecommendedShortAgyPrefersLowEffortOnTie(t *testing.T) {
	ids := []string{"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low", "gemini-3.1-pro-high"}
	prices := map[string]float64{
		"agy/gemini-3.8-flash-high": 1.125, "agy/gemini-3.8-flash-medium": 1.125, "agy/gemini-3.8-flash-low": 1.125,
		"agy/gemini-3.1-pro-high": 3.2,
	}
	useCatalogs(t, nil, ids, nil, prices)
	if got := recommendedUtilityModel(session.KindAgy); got != "gemini-3.8-flash-low" {
		t.Fatalf("agy short = %q, want the low-effort Flash", got)
	}
	// No prices: the fixed Flash default, resolved against the catalog — which here does not
	// list it, so the answer is the CLI default, which is what would run.
	useCatalogs(t, nil, ids, nil, map[string]float64{})
	if got := recommendedUtilityModel(session.KindAgy); got != "" {
		t.Fatalf("agy short without prices = %q, want \"\" (Flash Medium is not listed)", got)
	}
}

// #972 review round 2: agy ≥1.1.19 lists "<id>\t<display name>", and the run path passes only
// catalog ids through (agyChatModel). The recommendation must name the id, or the screen says
// Flash while the CLI default runs.
func TestAgyRecommendationUsesCatalogID(t *testing.T) {
	prev := agyModels
	t.Cleanup(func() { agyModels = prev })
	twoColumn := []agents.ModelChoice{
		{ID: "gemini-3.7-flash-high", Label: "Gemini 3.7 Flash (High)"},
		{ID: "gemini-3.5-flash-medium", Label: "Gemini 3.5 Flash (Medium)"},
		{ID: "gemini-3.5-flash-low", Label: "Gemini 3.5 Flash (Low)"},
	}
	agyModels = func() []agents.ModelChoice { return twoColumn }
	for _, got := range []string{recommendedAssistantModel(session.KindAgy), recommendedUtilityModel(session.KindAgy)} {
		if got != "gemini-3.5-flash-medium" {
			t.Fatalf("agy recommendation = %q, want the catalog id gemini-3.5-flash-medium", got)
		}
		if agyChatModel(got, twoColumn) != got {
			t.Fatalf("the run path drops %q", got)
		}
	}
	// The pre-1.1.19 one-column form (id == display name) keeps working unchanged.
	agyModels = func() []agents.ModelChoice {
		return []agents.ModelChoice{{ID: defaultAgyChatModel, Label: defaultAgyChatModel}}
	}
	if got := recommendedAssistantModel(session.KindAgy); got != defaultAgyChatModel {
		t.Fatalf("one-column agy = %q", got)
	}
	// No catalog at all (agy not logged in): the fixed name, as before.
	agyModels = func() []agents.ModelChoice { return nil }
	if got := recommendedAssistantModel(session.KindAgy); got != defaultAgyChatModel {
		t.Fatalf("agy without a catalog = %q", got)
	}
}

// chat / prose stay on a named tier — the newest "-luna" — and do NOT follow price: ranking by
// price there would quietly pick the weakest model for text a person reads.
func TestRecommendedCodexChatFollowsNewestLuna(t *testing.T) {
	// The price of luna is set ABOVE sol here, so a price ranking would pick sol.
	useCatalogs(t, codexCatalog0926, nil, nil, map[string]float64{"codex/gpt-6-luna": 9, "codex/gpt-6-sol": 1})
	if got := recommendedAssistantModel(session.KindCodex); got != "gpt-6-luna" {
		t.Fatalf("codex chat = %q, want the newest luna", got)
	}
	if got := RecommendedModels(session.KindCodex).Prose; got != "gpt-6-luna" {
		t.Fatalf("codex prose = %q, want the newest luna", got)
	}
	// A catalog that cannot be read keeps the fixed id it replaced.
	useCatalogs(t, nil, nil, nil, nil)
	if got := recommendedAssistantModel(session.KindCodex); got != defaultCodexChatModel {
		t.Fatalf("codex chat without a catalog = %q, want %q", got, defaultCodexChatModel)
	}
	// #972 review round 2: a READABLE catalog with no qualifying luna — the only one retiring,
	// or none listed — answers the CLI default, never the fixed id it may not serve.
	useCatalogs(t, []string{"gpt-6-sol", "gpt-5.6-luna"}, nil, map[string]bool{"gpt-5.6-luna": true}, nil)
	if got := recommendedAssistantModel(session.KindCodex); got != "" {
		t.Fatalf("codex chat with only a retiring luna = %q, want \"\"", got)
	}
	useCatalogs(t, []string{"gpt-6-astra", "gpt-6-sol"}, nil, nil, nil)
	if got := recommendedAssistantModel(session.KindCodex); got != "" {
		t.Fatalf("codex chat with no luna listed = %q, want \"\"", got)
	}
}

// RecommendedModels is what the Console draws "推奨（現在: X）" from; it must be the same
// functions the run paths call, tier by tier.
func TestRecommendedModelsMatchRunPaths(t *testing.T) {
	useCatalogs(t, codexCatalog0926, nil, nil, codexPrices0926)
	for _, kind := range []string{session.KindClaude, session.KindCodex, session.KindCursor, session.KindAgy} {
		got := RecommendedModels(kind)
		if want := ResolveChatModel(kind, ""); got.Chat != want {
			t.Errorf("%s chat = %q, the chat path resolves %q", kind, got.Chat, want)
		}
		if want := recommendedOneShotModel(kind, OneShotProse); got.Prose != want {
			t.Errorf("%s prose = %q, the one-shot path resolves %q", kind, got.Prose, want)
		}
		if want := recommendedOneShotModel(kind, OneShotShort); got.Short != want {
			t.Errorf("%s short = %q, the one-shot path resolves %q", kind, got.Short, want)
		}
	}
}

func TestNewestTierModel(t *testing.T) {
	cases := []struct {
		ids  []string
		want string
	}{
		{[]string{"gpt-5.6-luna", "gpt-6-luna", "gpt-6-sol"}, "gpt-6-luna"},
		{[]string{"gpt-6-luna", "gpt-6.1-luna", "gpt-5.10-luna"}, "gpt-6.1-luna"},
		{[]string{"gpt-5.10-luna", "gpt-5.9-luna"}, "gpt-5.10-luna"}, // numeric, not lexical
		{[]string{"gpt-6-luna-preview", "gpt-latest-luna", "gpt-5.6-luna"}, "gpt-5.6-luna"},
		{[]string{"gpt-6-sol"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := newestTierModel(c.ids, "gpt-", "luna"); got != c.want {
			t.Errorf("newestTierModel(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}

// #972 review: a member who picked the CLI default ("" configured) must run with no -m, even
// when a cheaper model is priced — the screen says Default. Only an unset setting (or the
// "recommended" sentinel, resolved by the caller) gets the recommendation.
func TestCodexOneShotModelRespectsExplicitDefault(t *testing.T) {
	t.Setenv("AF_TITLE_MODEL_CODEX", "")
	useCatalogs(t, codexCatalog0926, nil, nil, codexPrices0926)
	if m, auto := codexOneShotModel("", true, false, OneShotShort); m != "" || auto {
		t.Fatalf("explicit Default ran %q (auto=%v), want no -m", m, auto)
	}
	if args := codexOneShotArgsFor(""); argValue(args, "-m") != "" {
		t.Fatalf("argv re-picked a model: %q", args)
	}
	if m, auto := codexOneShotModel("", false, false, OneShotShort); m != "gpt-6-luna" || !auto {
		t.Fatalf("unset = %q (auto=%v), want the recommendation gpt-6-luna as our own pick", m, auto)
	}
	if m, auto := codexOneShotModel("gpt-6-sol", true, false, OneShotShort); m != "gpt-6-sol" || auto {
		t.Fatalf("explicit model = %q (auto=%v)", m, auto)
	}
	// The operator's AF_TITLE_MODEL_CODEX stands in for an UNSET setting only, and is never
	// treated as our own pick (a failure must not quietly drop it).
	t.Setenv("AF_TITLE_MODEL_CODEX", "env-model")
	if m, auto := codexOneShotModel("", false, false, OneShotShort); m != "env-model" || auto {
		t.Fatalf("unset with env = %q (auto=%v), want env-model, not ours", m, auto)
	}
	// Round 2: neither an explicit Default nor a "recommended" that resolved to nothing may turn
	// into the environment's model — and an empty pick is nothing to retry without.
	if m, auto := codexOneShotModel("", true, false, OneShotShort); m != "" || auto {
		t.Fatalf("explicit Default with env = %q (auto=%v), want no -m", m, auto)
	}
	if m, auto := codexOneShotModel("", true, true, OneShotShort); m != "" || auto {
		t.Fatalf("empty recommendation with env = %q (auto=%v), want no -m and not ours", m, auto)
	}
}

// #972 review round 3: a codex read that succeeded with nothing listed is not "no catalog" —
// the fixed id must not come back as if it existed.
func TestCodexChatEmptyCatalogIsNotUnreadable(t *testing.T) {
	useCatalogs(t, []string{}, nil, nil, nil)
	if got := recommendedAssistantModel(session.KindCodex); got != "" {
		t.Fatalf("codex chat over an empty catalog = %q, want \"\"", got)
	}
}

// #972 review rounds 3–4: claude's chat always passes --model. With sonnet hidden the
// recommendation moves to the next visible tier, and a conversation with no model of its own no
// longer falls back to claude-sonnet-5 (which "sonnet" hides too).
func TestRecommendedClaudeSkipsHiddenTier(t *testing.T) {
	prev := deps.VisibleModel
	t.Cleanup(func() { deps.VisibleModel = prev })
	deps.VisibleModel = func(_, model string) string {
		if strings.Contains(model, "sonnet") {
			return ""
		}
		return model
	}
	t.Setenv("AF_CHAT_MODEL", "")
	got := RecommendedModels(session.KindClaude)
	if got.Chat != "opus" || got.Prose != "opus" || got.Short != "haiku" {
		t.Fatalf("claude with sonnet hidden = %+v, want chat/prose opus, short haiku", got)
	}
	if m := chatModel(&ChatConversation{}); m != "opus" {
		t.Fatalf("a conversation with no model runs %q, want opus (not the hidden claude-sonnet-5)", m)
	}
	if m := chatModel(&ChatConversation{Model: ResolveChatModel(session.KindClaude, "")}); m != got.Chat {
		t.Fatalf("a new conversation runs %q, the answer says %q", m, got.Chat)
	}
	// Short prefers haiku; hiding it moves to sonnet — here hidden too — then opus.
	deps.VisibleModel = func(_, model string) string {
		if model == "haiku" || model == "sonnet" {
			return ""
		}
		return model
	}
	if got := RecommendedModels(session.KindClaude).Short; got != "opus" {
		t.Fatalf("claude short with haiku and sonnet hidden = %q, want opus", got)
	}
}

// #972 review round 4: an operator override the member hid, or an agy name the catalog does not
// list, cannot be what runs — the computed recommendation applies and is what the screen names.
// An agy display name is resolved to its catalog id.
func TestOneShotEnvOverrideIsValidated(t *testing.T) {
	useCatalogs(t, codexCatalog0926, nil, nil, codexPrices0926)
	prev := deps.VisibleModel
	t.Cleanup(func() { deps.VisibleModel = prev })
	deps.VisibleModel = func(_, model string) string {
		if model == "gpt-6-sol" {
			return ""
		}
		return model
	}
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-6-sol")
	if got := RecommendedModels(session.KindCodex).Short; got != "gpt-6-luna" {
		t.Fatalf("hidden override = %q, want the computed gpt-6-luna", got)
	}
	if m, auto := codexOneShotModel("", false, false, OneShotShort); m != "gpt-6-luna" || !auto {
		t.Fatalf("unset with a hidden override = %q (auto=%v), want our own gpt-6-luna", m, auto)
	}

	prevAgy := agyModels
	t.Cleanup(func() { agyModels = prevAgy })
	agyModels = func() []agents.ModelChoice {
		return []agents.ModelChoice{
			{ID: "gemini-3.5-flash-medium", Label: "Gemini 3.5 Flash (Medium)"},
			{ID: "gemini-3.7-flash-high", Label: "Gemini 3.7 Flash (High)"},
		}
	}
	t.Setenv("AF_TITLE_MODEL_AGY", "Gemini 3.7 Flash (High)")
	if got := RecommendedModels(session.KindAgy).Short; got != "gemini-3.7-flash-high" {
		t.Fatalf("agy display-name override = %q, want its id", got)
	}
	t.Setenv("AF_TITLE_MODEL_AGY", "Gemini 9 Ultra")
	if got := RecommendedModels(session.KindAgy).Short; got != "gemini-3.5-flash-medium" {
		t.Fatalf("unlisted agy override = %q, want the computed Flash Medium", got)
	}
}

// #972 review round 3: the operator's AF_TITLE_MODEL_<KIND> is part of the recommendation — so
// it takes effect for "recommended" (every Console default) and is what the screen names — and
// it is never treated as our own pick to retry away.
func TestOneShotEnvOverridesRecommendation(t *testing.T) {
	useCatalogs(t, codexCatalog0926, nil, nil, codexPrices0926)
	t.Setenv("AF_TITLE_MODEL_CODEX", "gpt-6-sol")
	got := RecommendedModels(session.KindCodex)
	if got.Short != "gpt-6-sol" || got.Prose != "gpt-6-sol" {
		t.Fatalf("recommended with AF_TITLE_MODEL_CODEX = %+v, want gpt-6-sol for both one-shot tiers", got)
	}
	if m, auto := codexOneShotModel("", false, false, OneShotShort); m != "gpt-6-sol" || auto {
		t.Fatalf("unset = %q (auto=%v), want the operator's model, not ours", m, auto)
	}
	t.Setenv("AF_TITLE_MODEL_CODEX", "")
	if got := RecommendedModels(session.KindCodex).Short; got != "gpt-6-luna" {
		t.Fatalf("without the override = %q", got)
	}
}
