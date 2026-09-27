package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const modelInfoCatalogFixture = `{
 "openai": {"models": {
   "gpt-6-luna": {"release_date": "2026-09-22", "limit": {"context": 1050000, "output": 128000},
                  "cost": {"input": 0.1, "output": 0.5, "cache_read": 0.01}},
   "gpt-5.5":    {"release_date": "2026-04-23", "cost": {"input": 5, "output": 30}},
   "gpt-4o-mini": {"status": "deprecated", "cost": {"input": 0.15, "output": 0.6}},
   "gpt-5":      {"cost": {"input": 1.25, "output": 10}}
 }},
 "opencode": {"models": {
   "glm-5":     {"limit": {"context": 204800}, "cost": {"input": 1, "output": 3.2}},
   "hy3-free":  {"cost": {"input": 0, "output": 0}}
 }},
 "zhipuai": {"models": {
   "glm-5":     {"cost": {"input": 0.6, "output": 2.2}}
 }},
 "anthropic": {"models": {
   "claude-opus-4-8": {"release_date": "2026-05-28", "limit": {"context": 1000000}, "cost": {"input": 5, "output": 25}},
   "opus":            {"cost": {"input": 99, "output": 99}}
 }},
 "google": {"models": {
   "gemini-3.8-flash": {"limit": {"context": 1048576}, "cost": {"input": 0.75, "output": 3.75}}
 }}
}`

func TestResolveModelInfo(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, modelInfoCatalogFixture)
	codexRetirement = func(id string) (agents.ModelRetiring, bool) {
		if id == "gpt-5.5" {
			return agents.ModelRetiring{At: "2026-10-14T19:00:00Z", Successor: "gpt-5.6-sol"}, true
		}
		return agents.ModelRetiring{}, false
	}
	t.Cleanup(func() { codexRetirement = defaultCodexRetirement })

	cases := []struct {
		kind, id string
		want     *agents.ModelInfo
	}{
		{session.KindCodex, "gpt-6-luna", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 0.1, Out: 0.5, CacheRead: 0.01}, PriceFrom: "openai",
			Context: 1050000, Released: "2026-09-22",
		}},
		{session.KindCodex, "gpt-5.5", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 5, Out: 30}, PriceFrom: "openai", Released: "2026-04-23",
			Retiring: &agents.ModelRetiring{At: "2026-10-14T19:00:00Z", Successor: "gpt-5.6-sol"},
		}},
		{session.KindCodex, "gpt-4o-mini", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 0.15, Out: 0.6}, PriceFrom: "openai", Deprecated: true,
		}},
		{session.KindCodex, "gpt-unknown", nil},
		// opencode is priced at the gateway it is billed through, not the maker's own price.
		{session.KindOpencode, "opencode-go/glm-5", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 1, Out: 3.2}, PriceFrom: "opencode", Context: 204800,
		}},
		// A free model is a real $0, not "unknown".
		{session.KindOpencode, "opencode/hy3-free", &agents.ModelInfo{
			Price: &agents.ModelPrice{}, PriceFrom: "opencode",
		}},
		// agy's effort variants and display names land on the base model.
		{session.KindAgy, "Gemini 3.8 Flash (Low)", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 0.75, Out: 3.75}, PriceFrom: "google", Context: 1048576,
		}},
		// A registered full claude id is looked up; a tier alias never is, even when some
		// catalog row happens to carry that name.
		{session.KindClaude, "claude-opus-4-8", &agents.ModelInfo{
			Price: &agents.ModelPrice{In: 5, Out: 25}, PriceFrom: "anthropic", Context: 1000000, Released: "2026-05-28",
		}},
		{session.KindClaude, "opus", nil},
		// No price source: the fallback providers would otherwise answer for these.
		{session.KindCursor, "gpt-5", nil},
		{session.KindMuse, "gpt-5", nil},
		{session.KindLcpp, "gpt-5", nil},
		{session.KindCodex, "", nil},
	}
	for _, c := range cases {
		if got := resolveModelInfo(c.kind, c.id); !reflect.DeepEqual(got, c.want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(c.want)
			t.Errorf("resolveModelInfo(%s, %q) = %s, want %s", c.kind, c.id, gj, wj)
		}
	}
}

// Info is served only on ?info=1: MCP list_models reads the same route and must not grow.
func TestAgentModelsInfoOnRequest(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, modelInfoCatalogFixture)
	writeUIPrefs(t, `{"hiddenModels":{},"claudeCustomModels":["claude-opus-4-8"]}`)
	read := func(query string) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/agents/claude/models"+query, nil)
		req.SetPathValue("kind", "claude")
		rec := httptest.NewRecorder()
		handleAgentModels(rec, req)
		var got struct {
			Models []struct {
				ID   string          `json:"id"`
				Info json.RawMessage `json:"info"`
			} `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", rec.Body.String(), err)
		}
		out := map[string]json.RawMessage{}
		for _, m := range got.Models {
			out[m.ID] = m.Info
		}
		return out
	}
	for id, info := range read("") {
		if info != nil {
			t.Errorf("without ?info=1, %s carries info %s", id, info)
		}
	}
	got := read("?info=1&custom=" + url.QueryEscape(`["claude-opus-4-8"]`))
	if got["claude-opus-4-8"] == nil {
		t.Errorf("?info=1: the registered model has no info (%v)", got)
	}
	if got["opus"] != nil {
		t.Errorf("?info=1: the alias opus carries info %s", got["opus"])
	}
}
