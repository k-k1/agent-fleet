package main

// Who MADE the model behind a launch id, served as ModelChoice.Provider by
// GET /agents/{kind}/models. The Console draws the maker's brand mark beside each entry in
// the model picker, which is what makes opencode's flat catalog (~59 ids in one list)
// scannable by eye.
//
// Two things this deliberately is not:
//
//  1. NOT the id's prefix. An opencode id names the BILLING ROUTE — "opencode/…" is Zen
//     (metered) and "opencode-go/…" is the Go subscription — not the maker. Taking the
//     prefix would paint the whole catalog with one gateway mark, i.e. exactly the list the
//     picker needs to tell apart. Only a prefix that is itself a maker ("anthropic/…") is
//     taken at face value.
//
//  2. NOT "which provider does models.dev list this id under". Upstream has no vendor field,
//     and its hosters carry everyone else's weights. Measured against the shipped catalog:
//     `glm-5.2` is listed by alibaba, amazon-bedrock, google-vertex, mistral, nvidia AND
//     zhipuai, so resolving by "who has this id" attributes GLM to Alibaba, Kimi to Bedrock
//     and Ling to NVIDIA. A wrong brand mark next to a model someone is about to pay for is
//     worse than no mark at all.
//
// What is used instead is the catalog's `family` ("glm", "kimi-k2", "gpt-codex") — real data,
// and the one field that survives re-hosting because every hoster copies it with the weights
// — mapped to its maker by the reviewed table below. The table is the editorial part, so
// model_provider_test.go pins it against a real catalog: a product line that stops resolving,
// or a new one nobody mapped, fails there rather than showing the wrong logo.
//
// An id that resolves to nothing carries no provider, and the Console shows no mark. That is
// the intended outcome for opencode's free-tier oddities (hy3, ling, mimo, big-pickle …),
// whose makers have no logo in the icon set anyway.

import (
	"sort"
	"strings"
)

// modelVendorFamilies maps a product line to the provider id whose mark is drawn. The key is
// matched as a PREFIX of the family's (or the id's) leading "-"-separated segment, because
// upstream writes some lines with the version glued on: "qwen3.5-plus" has to find "qwen".
// The longest matching key wins, which is what keeps "ministral" off "mistral".
var modelVendorFamilies = map[string]string{
	"claude":    "anthropic",
	"gpt":       "openai",
	"codex":     "openai",
	"gemini":    "google",
	"gemma":     "google",
	"lyria":     "google",
	"veo":       "google",
	"qwen":      "alibaba",
	"qvq":       "alibaba",
	"glm":       "zhipuai",
	"kimi":      "moonshotai",
	"deepseek":  "deepseek",
	"grok":      "xai",
	"mistral":   "mistral",
	"mixtral":   "mistral",
	"ministral": "mistral",
	"magistral": "mistral",
	"codestral": "mistral",
	"devstral":  "mistral",
	"pixtral":   "mistral",
	"voxtral":   "mistral",
	"minimax":   "minimax",
	"command":   "cohere",
	"north":     "cohere",
	"nemotron":  "nvidia",
	"llama":     "meta",
	"muse":      "meta",
	"nova":      "amazon-bedrock",
	"titan":     "amazon-bedrock",
}

// modelVendorMarks is every provider id that may be ANSWERED with, and the mark it is drawn
// as. google-vertex is Google's own models under a different endpoint, so it folds onto the
// Google mark rather than being a separate brand. The gateways are deliberately not in here
// even though the Console has an opencode and a copilot mark elsewhere: those marks say which
// CLI ran, and reusing them here would claim the gateway built the model.
var modelVendorMarks = map[string]string{
	"anthropic":      "anthropic",
	"openai":         "openai",
	"google":         "google",
	"google-vertex":  "google",
	"alibaba":        "alibaba",
	"zhipuai":        "zhipuai",
	"moonshotai":     "moonshotai",
	"deepseek":       "deepseek",
	"xai":            "xai",
	"mistral":        "mistral",
	"minimax":        "minimax",
	"cohere":         "cohere",
	"nvidia":         "nvidia",
	"meta":           "meta",
	"amazon-bedrock": "amazon-bedrock",
}

// modelGateways are routes that resell many makers' models. Their id prefix says where the
// request is billed, never who built the thing, so it is never taken as the maker — even
// though opencode and github-copilot do have marks of their own elsewhere in the Console.
var modelGateways = map[string]bool{
	"opencode":       true,
	"opencode-go":    true,
	"openrouter":     true,
	"github-copilot": true,
}

// modelFamilyProviders is whose model lists get their families indexed out of the catalog:
// the makers (to place a bare id like codex's "gpt-5.6-luna") and the gateways (to place
// "opencode-go/glm-5.2", whose family only exists under the gateway's own entry). Listing
// them keeps the index around 20 providers instead of all 212 — the agent holds it for the
// life of the catalog cache.
var modelFamilyProviders = []string{
	"anthropic", "openai", "google", "google-vertex", "alibaba", "zhipuai", "moonshotai",
	"deepseek", "xai", "mistral", "minimax", "cohere", "nvidia", "meta", "amazon-bedrock",
	"opencode", "opencode-go", "github-copilot", "openrouter",
}

// modelKindVendor is the maker a CLI implies on its own, for ids no catalog can place: codex
// only ever runs OpenAI models, agy only Google's. Gateways are deliberately absent — opencode,
// copilot and cursor each route to many makers, so falling back to their own mark would
// attribute someone else's model to the gateway.
var modelKindVendor = map[string]string{
	"claude": "anthropic",
	"codex":  "openai",
	"agy":    "google",
	"kiro":   "amazon-bedrock",
}

// modelFamilyIndexed is the set form of modelFamilyProviders, for the catalog parser.
func modelFamilyIndexed() map[string]bool {
	m := make(map[string]bool, len(modelFamilyProviders))
	for _, p := range modelFamilyProviders {
		m[p] = true
	}
	return m
}

// modelVendorKeys is modelVendorFamilies' keys, longest first (ties broken alphabetically so
// the order cannot wander between runs). Built once: this is consulted per catalog entry.
var modelVendorKeys = func() []string {
	out := make([]string, 0, len(modelVendorFamilies))
	for k := range modelVendorFamilies {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}()

// modelVendorForName resolves a family or model name to a maker through its leading segment
// ("gpt-codex" -> "gpt", "qwen3.5-plus" -> "qwen3.5" -> "qwen"). "" when nothing matches.
func modelVendorForName(name string) string {
	head := strings.ToLower(strings.TrimSpace(name))
	if i := strings.Index(head, "-"); i > 0 {
		head = head[:i]
	}
	if head == "" {
		return ""
	}
	for _, k := range modelVendorKeys {
		if strings.HasPrefix(head, k) {
			return modelVendorFamilies[k]
		}
	}
	return ""
}

// modelCatalogFamily looks a model's family up, preferring the route it is actually offered
// through (that is where a gateway-only id like "glm-5-free" is described) and otherwise
// scanning the indexed providers in a fixed order.
func modelCatalogFamily(route, name string) string {
	cat := loadUsageCatalog()
	if cat == nil || len(cat.family) == 0 || name == "" {
		return ""
	}
	if route != "" {
		if f := cat.family[route+"/"+name]; f != "" {
			return f
		}
	}
	for _, p := range modelFamilyProviders {
		if f := cat.family[p+"/"+name]; f != "" {
			return f
		}
	}
	return ""
}

// resolveModelProvider answers which maker's mark belongs to a launch id, or "" when nothing
// can be said. Callers must treat "" as "draw no mark", never as a default vendor.
func resolveModelProvider(kind, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "" // the Default entry is not a model
	}
	route, name := "", id
	if i := strings.Index(id, "/"); i > 0 {
		route, name = id[:i], id[i+1:]
	}
	// A prefix that names a maker is the maker. A gateway prefix is not.
	if route != "" && !modelGateways[route] {
		if v, ok := modelVendorMarks[route]; ok {
			return v
		}
	}
	if v := modelVendorForName(modelCatalogFamily(route, name)); v != "" {
		return v
	}
	// No catalog, or a model upstream has not described: the id's own leading segment is the
	// same signal one step weaker ("gpt-5.6-luna" is still visibly a GPT).
	if v := modelVendorForName(name); v != "" {
		return v
	}
	return modelKindVendor[kind]
}
