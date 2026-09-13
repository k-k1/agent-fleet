package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shape that matters for model_provider.go: the same id offered by a maker and by
// hosters, and gateway-only ids that exist under no maker at all. `cost` is present only
// because a catalog with no priced model is not treated as a catalog (parseUsageCatalog).
//
// The trap this fixture exists for: alibaba, nvidia and amazon-bedrock all list `glm-5.2`,
// so "which provider has this id" answers Alibaba. The family says "glm", which is Zhipu's
// line wherever it is hosted.
const providerCatalogFixture = `{
 "anthropic": {"models": {
   "claude-opus-5":    {"family": "claude-opus",   "cost": {"input": 5, "output": 25}},
   "claude-sonnet-4-6":{"family": "claude-sonnet", "cost": {"input": 2, "output": 10}}
 }},
 "openai": {"models": {
   "gpt-5.6-luna": {"family": "gpt", "cost": {"input": 1, "output": 6}}
 }},
 "zhipuai": {"models": {
   "glm-5.3": {"family": "glm", "cost": {"input": 1.4, "output": 4.4}}
 }},
 "alibaba": {"models": {
   "glm-5.2":     {"family": "glm",  "cost": {"input": 1.2, "output": 4}},
   "qwen3.8-max": {"family": "qwen", "cost": {"input": 1.2, "output": 4}}
 }},
 "nvidia": {"models": {
   "glm-5.2": {"family": "glm", "cost": {"input": 1.1, "output": 3.9}}
 }},
 "amazon-bedrock": {"models": {
   "claude-sonnet-4-6": {"family": "claude-sonnet", "cost": {"input": 2.2, "output": 11}}
 }},
 "opencode": {"models": {
   "claude-sonnet-4-6": {"family": "claude-sonnet", "cost": {"input": 2.5, "output": 12.5}},
   "glm-5-free":        {"family": "glm-free",      "cost": {"input": 0, "output": 0}},
   "x-preview-f-free":  {"family": "glm",           "cost": {"input": 0, "output": 0}},
   "big-pickle":        {"family": "big-pickle",    "cost": {"input": 0, "output": 0}}
 }},
 "opencode-go": {"models": {
   "glm-5.2": {"family": "glm", "cost": {"input": 0, "output": 0}}
 }}
}`

func TestResolveModelProvider(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, providerCatalogFixture)

	cases := []struct {
		name, kind, id, want string
	}{
		// The headline case. Three hosters list this id; only the family names the maker.
		{"gateway id resolves to the maker, not a hoster", "opencode", "opencode-go/glm-5.2", "zhipuai"},
		{"metered route resolves the same way", "opencode", "opencode/claude-sonnet-4-6", "anthropic"},
		// A free-tier line upstream suffixes ("glm-free"); the leading segment still says glm.
		{"free-tier family keeps its product line", "opencode", "opencode/glm-5-free", "zhipuai"},
		// Positive control for the catalog path: the id says nothing, so a non-empty answer
		// can only have come from the indexed family. If the family index were empty this
		// would be "" and the test would fail rather than pass by another route.
		{"family is what answers, not the id text", "opencode", "opencode/x-preview-f-free", "zhipuai"},
		// A maker's own prefix is taken at face value; a gateway's never is.
		{"a maker prefix is the maker", "opencode", "anthropic/claude-opus-5", "anthropic"},
		{"an unplaceable gateway id gets no mark", "opencode", "opencode/big-pickle", ""},
		// Bare ids: codex's catalog has no prefix at all.
		{"bare id resolves by family", "codex", "gpt-5.6-luna", "openai"},
		{"claude's tier alias falls back to the kind", "claude", "opus", "anthropic"},
		{"kiro's auto falls back to the kind", "kiro", "auto", "amazon-bedrock"},
		// copilot routes to many makers, so it has no kind fallback: "auto" says nothing.
		{"copilot's auto gets no mark", "copilot", "auto", ""},
		{"the Default entry is not a model", "opencode", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveModelProvider(c.kind, c.id); got != c.want {
				t.Fatalf("resolveModelProvider(%q, %q) = %q, want %q", c.kind, c.id, got, c.want)
			}
		})
	}
}

// With no catalog at all (a workspace that never ran opencode) the family is unavailable, so
// the id's own leading segment has to carry what it can. This is the degraded path the
// picker runs on for most users, so it gets its own test rather than being assumed.
func TestResolveModelProviderWithoutCatalog(t *testing.T) {
	useIsolatedUsageDir(t)
	t.Setenv("AF_USAGE_CATALOG", "/nonexistent/models.json")
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // keep the machine's own opencode cache out of it
	resetUsageCatalogCache(t)

	if cat := loadUsageCatalog(); cat != nil {
		t.Fatalf("expected no catalog, got one from %q", cat.origin) // the premise of this test
	}
	cases := []struct{ kind, id, want string }{
		{"opencode", "opencode-go/glm-5.2", "zhipuai"},
		{"opencode", "opencode/claude-sonnet-4-6", "anthropic"},
		{"codex", "gpt-5.6-luna", "openai"},
		{"claude", "opus", "anthropic"},
		// Nothing in the name and nothing from the kind: still no wrong guess.
		{"opencode", "opencode/big-pickle", ""},
	}
	for _, c := range cases {
		if got := resolveModelProvider(c.kind, c.id); got != c.want {
			t.Errorf("no catalog: resolveModelProvider(%q, %q) = %q, want %q", c.kind, c.id, got, c.want)
		}
	}
}

// Every maker the tables can name must have a mark, or the Console is asked to draw a logo
// it does not have and silently renders nothing. This is the only thing that notices, because
// the two sides live in different languages.
func TestModelVendorTablesOnlyNameMarkedProviders(t *testing.T) {
	for family, vendor := range modelVendorFamilies {
		if _, ok := modelVendorMarks[vendor]; !ok {
			t.Errorf("family %q maps to %q, which has no mark", family, vendor)
		}
	}
	for kind, vendor := range modelKindVendor {
		if _, ok := modelVendorMarks[vendor]; !ok {
			t.Errorf("kind %q falls back to %q, which has no mark", kind, vendor)
		}
	}
	// A mark that nothing can ever resolve to is dead weight: it would never be drawn.
	reachable := map[string]bool{}
	for _, v := range modelVendorFamilies {
		reachable[v] = true
	}
	for _, v := range modelKindVendor {
		reachable[v] = true
	}
	for p, v := range modelVendorMarks {
		if !reachable[v] {
			t.Errorf("mark %q (via provider %q) is unreachable: no family or kind resolves to it", v, p)
		}
	}
	// A gateway must never be answerable: its mark would say "opencode made this model".
	for p := range modelGateways {
		if _, ok := modelVendorMarks[p]; ok {
			t.Errorf("gateway %q is in modelVendorMarks, so a route prefix could be reported as the maker", p)
		}
	}
}

// Longest-key-first matching is what keeps one product line off another's mark. Pinned
// because the ordering is computed, not written down.
func TestModelVendorForNamePrefersTheLongestLine(t *testing.T) {
	cases := []struct{ name, want string }{
		{"ministral-8b", "mistral"}, // not mistral's own line, despite sharing five letters
		{"minimax-m2.5", "minimax"}, // "mini…" again, a different maker
		{"mistral-large", "mistral"},
		{"qwen3.5-plus", "alibaba"}, // version glued to the line name
		{"gpt-codex", "openai"},
		{"claude-sonnet", "anthropic"},
		{"", ""},
		{"nothing-here", ""},
	}
	for _, c := range cases {
		if got := modelVendorForName(c.name); got != c.want {
			t.Errorf("modelVendorForName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// Drift check against the catalog on this machine, when there is one. It does not run in CI
// (no opencode cache there), so it is a developer's tripwire for upstream introducing a
// product line nobody mapped — not a gate. The floor is deliberately below what is measured
// today (86/102 = 84% of the opencode route; the 16 that do not resolve are free-tier lines
// whose makers — Tencent, Xiaomi, inclusionAI, Meituan — have no logo upstream either) so
// that normal churn does not fail it.
func TestResolveModelProviderAgainstRealCatalog(t *testing.T) {
	// Resolve the machine's own cache path BEFORE isolating: useIsolatedUsageDir repoints
	// HOME and XDG_CACHE_HOME at temp dirs, which is what the other tests want and would
	// silently turn this one into a permanent skip.
	real := realOpencodeCatalogPath()
	useIsolatedUsageDir(t)
	if real == "" {
		t.Skip("no models.dev catalog on this machine (opencode never ran here)")
	}
	t.Setenv("AF_USAGE_CATALOG", real)
	resetUsageCatalogCache(t)
	cat := loadUsageCatalog()
	if cat == nil {
		t.Skipf("the catalog at %s did not parse as one", real)
	}

	// Read the same file again for the raw id list: the parsed catalog keeps families, not
	// the route's own membership.
	var raw map[string]struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	b, err := os.ReadFile(real)
	if err != nil {
		t.Skipf("catalog vanished between load and read: %v", err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Skipf("catalog is not the expected shape: %v", err)
	}
	ids := raw["opencode"].Models
	if len(ids) < 20 {
		t.Skipf("opencode route lists %d models, too few to measure", len(ids))
	}

	var unresolved []string
	for id := range ids {
		if resolveModelProvider("opencode", "opencode/"+id) == "" {
			unresolved = append(unresolved, id)
		}
	}
	got := len(ids) - len(unresolved)
	if pct := got * 100 / len(ids); pct < 75 {
		t.Errorf("only %d/%d (%d%%) of the opencode route resolves to a maker; unmapped lines: %s",
			got, len(ids), pct, strings.Join(unresolved, " "))
	}
	t.Logf("real catalog: %d/%d resolved; unmapped: %s", got, len(ids), strings.Join(unresolved, " "))
}

// realOpencodeCatalogPath is opencode's own cache as seen from the UNMODIFIED environment,
// or "" when it is not there.
func realOpencodeCatalogPath() string {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cache = filepath.Join(home, ".cache")
	}
	p := filepath.Join(cache, "opencode", "models.json")
	if st, err := os.Stat(p); err != nil || st.IsDir() || st.Size() == 0 {
		return ""
	}
	return p
}
