package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const recommendCatalogFixture = `{
 "openai": {"models": {
   "gpt-6-luna":   {"cost": {"input": 0.1, "output": 0.5}},
   "gpt-5.6-luna": {"cost": {"input": 0.2, "output": 1.2}},
   "gpt-4o-mini":  {"status": "deprecated", "cost": {"input": 0.15, "output": 0.6}}
 }},
 "google": {"models": {
   "gemini-3.8-flash": {"cost": {"input": 0.75, "output": 3.75}}
 }}
}`

// The comparable price is input + output/10, on the kind's own billing route; a model upstream
// marks deprecated is priced for the ledger but never offered to the recommendation.
func TestModelListPrice(t *testing.T) {
	useIsolatedUsageDir(t)
	useUsageCatalog(t, recommendCatalogFixture)
	if p, ok := modelListPrice(session.KindCodex, "gpt-6-luna"); !ok || math.Abs(p-0.15) > 1e-9 {
		t.Fatalf("gpt-6-luna = %v %v, want 0.15", p, ok)
	}
	if _, ok := modelListPrice(session.KindCodex, "gpt-4o-mini"); ok {
		t.Fatal("a deprecated model was offered to the recommendation")
	}
	if _, _, ok := usageCatalogLookup(session.KindCodex, "gpt-4o-mini"); !ok {
		t.Fatal("a deprecated model lost its ledger price")
	}
	if _, ok := modelListPrice(session.KindCodex, "gpt-unknown"); ok {
		t.Fatal("an unpriced model was offered to the recommendation")
	}
}

// agy's rows carry an effort variant, and older builds list display names; both must land on
// the base model's price.
func TestRecommendPriceIDAgy(t *testing.T) {
	for in, want := range map[string]string{
		"gemini-3.8-flash-low":         "gemini-3.8-flash",
		"gemini-3.8-flash":             "gemini-3.8-flash",
		"Gemini 3.8 Flash (Medium)":    "gemini-3.8-flash",
		"claude-sonnet-4-6-thinking":   "claude-sonnet-4-6",
		"Claude Sonnet 4.6 (Thinking)": "claude-sonnet-4-6",
	} {
		if got := recommendPriceID(session.KindAgy, in); got != want {
			t.Errorf("recommendPriceID(agy, %q) = %q, want %q", in, got, want)
		}
	}
	if got := recommendPriceID(session.KindCodex, "gpt-6-luna-low"); got != "gpt-6-luna-low" {
		t.Errorf("a non-agy id was rewritten: %q", got)
	}
	useIsolatedUsageDir(t)
	useUsageCatalog(t, recommendCatalogFixture)
	if _, ok := modelListPrice(session.KindAgy, "Gemini 3.8 Flash (Low)"); !ok {
		t.Fatal("an agy display name did not reach its price")
	}
}

// GET /agents/{kind}/models carries the Agent's own "recommended" per tier — the one the
// Console draws "推奨（現在: X）" from — and it follows the hidden-models setting exactly as
// the run paths do.
func TestAgentModelsCarriesRecommended(t *testing.T) {
	read := func() chatx.RecommendedSet {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/agents/claude/models", nil)
		req.SetPathValue("kind", "claude")
		rec := httptest.NewRecorder()
		handleAgentModels(rec, req)
		var got struct {
			Recommended *chatx.RecommendedSet `json:"recommended"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Recommended == nil {
			t.Fatalf("no recommended in %s (%v)", rec.Body.String(), err)
		}
		return *got.Recommended
	}
	writeUIPrefs(t, `{}`)
	if got, want := read(), (chatx.RecommendedSet{Chat: "sonnet", Prose: "sonnet", Short: "haiku"}); got != want {
		t.Fatalf("recommended = %+v, want %+v", got, want)
	}
	writeUIPrefs(t, `{"hiddenModels":{"claude":["haiku"]}}`)
	if got := read(); got.Short != "" {
		t.Fatalf("short = %q with haiku hidden, want \"\" (the CLI default runs)", got.Short)
	}
}

// The fetch replaces the copy only with something that parses as a catalog, and never fetches
// again while the copy is younger than a day.
func TestRefreshModelsDev(t *testing.T) {
	useIsolatedUsageDir(t)
	hits, body := 0, recommendCatalogFixture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	t.Setenv("AF_MODELS_DEV_URL", srv.URL)

	now := time.Now()
	refreshModelsDevIfStale(context.Background(), now)
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	resetUsageCatalogCache(t)
	if info := usageCatalogInfo(); info == nil || info.Origin != usageCatalogOriginFetched {
		t.Fatalf("catalog = %+v, want the fetched copy", info)
	}
	refreshModelsDevIfStale(context.Background(), now.Add(time.Hour))
	if hits != 1 {
		t.Fatalf("fetched again within a day: hits = %d", hits)
	}

	// A day later the upstream answers with something that is not a catalog: the good copy stays.
	body = "<html>maintenance</html>"
	refreshModelsDevIfStale(context.Background(), now.Add(25*time.Hour))
	if hits != 2 {
		t.Fatalf("hits = %d, want 2", hits)
	}
	b, err := os.ReadFile(modelsDevPath())
	if err != nil || string(b) != recommendCatalogFixture {
		t.Fatalf("the good copy was replaced: %q (%v)", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(modelsDevPath()), ".models.dev-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}

	t.Setenv("AF_MODELS_DEV_URL", "off")
	refreshModelsDevIfStale(context.Background(), now.Add(72*time.Hour))
	if hits != 2 {
		t.Fatal("AF_MODELS_DEV_URL=off still fetched")
	}
}
