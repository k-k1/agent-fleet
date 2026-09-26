package chatx

import (
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
	choices := func(ids []string) func() []agents.ModelChoice {
		return func() []agents.ModelChoice {
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
	useCatalogs(t, nil, ids, nil, map[string]float64{})
	if got := recommendedUtilityModel(session.KindAgy); got != defaultAgyChatModel {
		t.Fatalf("agy short without prices = %q, want %q", got, defaultAgyChatModel)
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
	t.Setenv("AF_TITLE_MODEL_CODEX", "env-model")
	if m, _ := codexOneShotModel("", false, false, OneShotShort); m != "" {
		t.Fatalf("with AF_TITLE_MODEL_CODEX the argv builder must be left to apply it, got %q", m)
	}
}
