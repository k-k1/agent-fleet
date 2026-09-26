package chatx

// What "recommended" resolves to, decided here and nowhere else (Issue #972). The Console used
// to carry its own copy of these rules (aiModelRow.tsx's recommendedModelId) to draw
// "推奨（現在: X）"; the two drifted, so it now draws X from RecommendedModels via
// GET /agents/{kind}/models instead of re-deriving it.
//
// Two rules, by what the tier needs (docs/log/84):
//
//   - short (titles, branch names, reply chips) — the CHEAPEST model the account lists, ranked
//     by the models.dev list price (modelListPrice). Any model will do for a label, so price is
//     the whole question, and ranking the live catalog follows every new generation without a
//     source edit: the fixed ids and name markers ("mini", "flash") this replaced had gone
//     stale — codex's catalog no longer lists any "mini", so its short tier ran on whatever
//     the CLI defaulted to.
//   - chat / prose — a fixed tier by NAME, newest version first (codex: the newest "-luna").
//     Quality matters there, and a price ranking would quietly pick the weakest model.
//
// What is never recommended differs by rule, on purpose: codex's own `upgrade` (retiring) notice
// excludes a model from both; models.dev's `status: deprecated` only from the short tier's price
// ranking (a deprecated row has no usable price). codex's catalog is the authority for what
// codex serves, and models.dev's openai rows describe the API, not the ChatGPT-backed CLI.
//
// Every rule degrades to the fixed id it replaced when the catalog or the prices are missing,
// so no price data means today's behaviour, never an empty pick where there used to be one.

import (
	"slices"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The live catalogs the rules read. Variables so this package's tests can answer for them: the
// real ones exec the CLI, and a test that did so would pass or fail by what is installed on the
// machine running it (memory: test-path-leaks-real-binary).
var (
	codexModels    = codex.Models
	codexRetiring  = codex.Retiring
	agyModels      = agy.Models
	museSafeModels = muse.SafeExecModels
)

// visibility is one reading of the hidden-models setting, applied to candidates: the saved one
// (prefsVisibility — the run paths), or a list a request supplies (explicitVisibility — the
// Console asking for its current, possibly unsaved, setting). Rules take it as a parameter so
// one answer is computed against one reading throughout.
type visibility struct {
	explicit     bool
	hidden       []string // effective list (fail-safe applied) when explicit
	claudeCustom []string // claude's registered models when explicit
}

var prefsVisibility = visibility{}

func explicitVisibility(kind string, raw, claudeCustom []string) visibility {
	return visibility{explicit: true, hidden: deps.EffectiveHidden(kind, raw, claudeCustom), claudeCustom: claudeCustom}
}

// claudeCustomModels is the registered-models list this reading was computed with.
func (v visibility) claudeCustomModels() []string {
	if !v.explicit {
		return uiprefs.ClaudeCustomModels()
	}
	return v.claudeCustom
}

func (v visibility) model(kind, m string) string {
	if !v.explicit {
		return visibleModel(kind, m)
	}
	if deps.ModelHiddenIn(v.hidden, m) {
		return ""
	}
	return m
}

func (v visibility) ids(kind string, ids []string) []string {
	if !v.explicit {
		return visibleModelIDs(kind, ids)
	}
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return deps.ModelHiddenIn(v.hidden, id) })
}

func (v visibility) list(kind string, l []agents.ModelChoice) []agents.ModelChoice {
	if !v.explicit {
		return filterVisibleModels(kind, l)
	}
	return slices.DeleteFunc(slices.Clone(l), func(c agents.ModelChoice) bool { return deps.ModelHiddenIn(v.hidden, c.ID) })
}

// RecommendedSet is what "recommended" resolves to for one kind, per tier — exactly the ids the
// run paths pass as --model ("" = no flag, the CLI's own default).
type RecommendedSet struct {
	Chat  string `json:"chat"`
	Prose string `json:"prose"`
	Short string `json:"short"`
}

// RecommendedModels answers for GET /agents/{kind}/models. It calls the same functions the run
// paths call, so the screen cannot show one model while another runs.
func RecommendedModels(kind string) RecommendedSet {
	return recommendedModels(prefsVisibility, kind)
}

// RecommendedModelsWithHidden answers for a hidden-models list and claude's registered models the
// caller supplies (raw, as the Console holds them) instead of the ones saved in ui-prefs — GET
// /agents/{kind}/models?hidden=…&custom=….
// The Console asks with its current setting, so the answer is exactly for what its screen shows,
// whether or not its debounced save has reached this Agent yet (#972 review, rounds 3–5: every
// scheme that matched a saved-prefs answer to the screen's setting after the fact had a race).
func RecommendedModelsWithHidden(kind string, raw, claudeCustom []string) RecommendedSet {
	return recommendedModels(explicitVisibility(kind, raw, claudeCustom), kind)
}

func recommendedModels(v visibility, kind string) RecommendedSet {
	chat := recommendedAssistantModelV(v, kind)
	if kind == session.KindMuse {
		// museChatModel's own fallback: the newest non-contributor row (ADR 0095 P2-21).
		chat = museSafeVisible(v)
	}
	return RecommendedSet{
		Chat:  chat,
		Prose: recommendedOneShotModelV(v, kind, OneShotProse),
		Short: recommendedOneShotModelV(v, kind, OneShotShort),
	}
}

// museSafeVisible is the newest non-data-sharing muse model this reading of the hidden-models
// setting leaves visible; "" when there is none (the catalog unread, no safe row, or every safe
// row hidden), and the chat then refuses the turn.
func museSafeVisible(v visibility) string {
	for _, id := range museSafeModels() {
		if m := v.model(session.KindMuse, id); m != "" {
			return m
		}
	}
	return ""
}

// claude's tier aliases in the order each purpose prefers them. claude has no "let the CLI pick"
// entry the member could see (its picker is the four aliases), and its chat always passes
// --model, so a hidden first choice moves to the next tier rather than to an unnamed CLI default
// that may itself be the hidden model (#972 review, round 4). Hiding all four is ignored by the
// hidden-models fail-safe (model_deny.go), so the first one is always the answer then.
var (
	claudeChatTiers  = []string{"sonnet", "opus", "haiku", "fable"}
	claudeShortTiers = []string{"haiku", "sonnet", "opus", "fable"}
)

// With every alias hidden, the fail-safe keeps the list in force only while a registered model
// is left (sessionx.EffectiveHidden), so that model is the answer — never a hidden alias (#972
// review, round 6). tiers[0] remains only for a state no reading of the setting produces.
func claudeFirstVisible(v visibility, tiers []string) string {
	for _, t := range tiers {
		if m := v.model(session.KindClaude, t); m != "" {
			return m
		}
	}
	for _, id := range v.claudeCustomModels() {
		if m := v.model(session.KindClaude, id); m != "" {
			return m
		}
	}
	return tiers[0]
}

// codexRecommendIDs is codex's live catalog as a recommendation may use it: minus hidden models
// and minus the ones codex itself announces are retiring. listed says whether the catalog could
// be read at all — an empty result from a readable catalog means "nothing qualifies", which must
// not be answered with a fixed id that may be retiring or absent (#972 review, round 2).
func codexRecommendIDs(v visibility) (ids []string, listed bool) {
	list := codexModels()
	// nil = never read (codex.Models returns its last good list, nil before the first); a read
	// that succeeded with nothing to list is an empty non-nil slice — still "listed", where the
	// fixed id must not come back (#972 review, round 3).
	ids = v.ids(session.KindCodex, modelChoiceIDs(list))
	return slices.DeleteFunc(ids, codexRetiring), list != nil
}

// codexNewestLuna is codex's chat / prose recommendation: the newest "-luna" in the catalog,
// "" when the catalog lists none that qualifies, and the fixed defaultCodexChatModel only when
// the catalog cannot be read (the CLI not logged in yet, say).
func codexNewestLuna(v visibility) string {
	ids, listed := codexRecommendIDs(v)
	if m := newestTierModel(ids, "gpt-", "luna"); m != "" || listed {
		return m
	}
	return v.model(session.KindCodex, defaultCodexChatModel)
}

// agyRecommendIDs is agy's live catalog minus hidden models.
func agyRecommendIDs(v visibility) []string {
	return v.ids(session.KindAgy, modelChoiceIDs(agyModels()))
}

// agyNamedModel resolves a fixed agy model NAME (defaultAgyChatModel, a display name) to the id
// the live catalog lists it under. agy ≥1.1.19 lists "<id>\t<display name>"
// (gemini-3.5-flash-medium / "Gemini 3.5 Flash (Medium)"), and agyChatModel drops a --model
// the catalog does not list as an id — so answering with the display name showed "Gemini 3.5
// Flash (Medium)" while the CLI default ran (#972 review, round 2). A readable catalog without
// that model answers "" (the CLI default, which is what runs); an unreadable one keeps the name,
// as before, which older agy builds accept as-is.
func agyNamedModel(v visibility, name string) string {
	all := agyModels()
	if len(all) == 0 {
		return v.model(session.KindAgy, name)
	}
	for _, m := range v.list(session.KindAgy, all) {
		if strings.EqualFold(m.ID, name) || strings.EqualFold(m.Label, name) {
			return m.ID
		}
	}
	return ""
}

// cheapestListedModel returns the id in ids with the lowest modelListPrice, "" when none is
// priced. Ties (agy lists one model once per reasoning effort, all at one price) go to the
// lowest effort — a label needs no reasoning — and then to catalog order, so the answer does
// not wander between calls.
func cheapestListedModel(kind string, ids []string) string {
	best, bestPrice, bestEffort := "", 0.0, 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		p, ok := modelListPrice(kind, id)
		if !ok {
			continue
		}
		e := effortRank(id)
		if best == "" || p < bestPrice || (p == bestPrice && e < bestEffort) {
			best, bestPrice, bestEffort = id, p, e
		}
	}
	return best
}

// effortRank orders effort variants of one model, lowest first; an id naming no effort sits in
// the middle.
func effortRank(id string) int {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "minimal"):
		return 0
	case strings.Contains(s, "low"):
		return 1
	case strings.Contains(s, "medium"):
		return 3
	case strings.Contains(s, "high"):
		return 4
	}
	return 2
}

// newestTierModel returns the newest "<prefix><version>-<tier>" id in ids — for codex,
// newestTierModel(ids, "gpt-", "luna") picks gpt-6-luna over gpt-5.6-luna. OpenAI keeps the tier
// names (astra / sol / terra / luna) across generations and moves only the version, so this
// follows a new generation without a source edit. "" when no id has that shape.
func newestTierModel(ids []string, prefix, tier string) string {
	best, bestVer := "", []int(nil)
	for _, id := range ids {
		ver, ok := strings.CutPrefix(id, prefix)
		if !ok {
			continue
		}
		if ver, ok = strings.CutSuffix(ver, "-"+tier); !ok {
			continue
		}
		v, ok := parseVersion(ver)
		if !ok {
			continue
		}
		if best == "" || compareVersion(v, bestVer) > 0 {
			best, bestVer = id, v
		}
	}
	return best
}

// parseVersion reads "6" / "5.6" / "5.6.1"; anything else (a date, a word) is not a version.
func parseVersion(s string) ([]int, bool) {
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// compareVersion compares numerically, a missing component counting as 0 (6 == 6.0 < 6.1).
func compareVersion(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
