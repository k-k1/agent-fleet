package codex

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Trimmed from a real `codex debug models` dump (0.144.1): visibility "hide"
// entries (codex-auto-review) must be dropped, order follows priority, and a
// missing display_name falls back to the slug.
func TestParseCatalog(t *testing.T) {
	in := []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":7,
		 "default_reasoning_effort":"medium","supported_reasoning_efforts":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
		{"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide","priority":43},
		{"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","visibility":"list","priority":1},
		{"slug":"gpt-x","visibility":"list","priority":99}
	]}`)
	got, err := parseCatalog(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []agents.ModelChoice{
		{ID: "gpt-5.6-sol", Label: "GPT-5.6-Sol"},
		{ID: "gpt-5.5", Label: "GPT-5.5", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
		{ID: "gpt-x", Label: "gpt-x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCatalog = %v, want %v", got, want)
	}
}

func TestParseCatalogBadJSON(t *testing.T) {
	if _, err := parseCatalog([]byte("not json")); err == nil {
		t.Fatal("want error on bad JSON")
	}
}
