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

// An `upgrade` on a catalog entry is codex's retirement notice; null or absent is not.
func TestParseRetiring(t *testing.T) {
	out := []byte(`{"models":[
	  {"slug":"gpt-6-luna","upgrade":null},
	  {"slug":"gpt-5.6-luna"},
	  {"slug":"gpt-5.5","upgrade":{"model":"gpt-5.6-sol","migration_markdown":"GPT-5.5 retires on October 14, 2026.","retirement_at":"2026-10-14T19:00:00Z"}}
	]}`)
	got := parseRetiring(out)
	want := map[string]agents.ModelRetiring{"gpt-5.5": {
		At: "2026-10-14T19:00:00Z", Note: "GPT-5.5 retires on October 14, 2026.", Successor: "gpt-5.6-sol",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRetiring = %v, want %v", got, want)
	}
	// A notice codex words differently still marks the model retiring.
	odd := parseRetiring([]byte(`{"models":[{"slug":"gpt-5.4","upgrade":"gpt-5.6-sol"}]}`))
	if _, ok := odd["gpt-5.4"]; !ok {
		t.Fatalf("parseRetiring(string upgrade) = %v, want gpt-5.4 marked", odd)
	}
	if parseRetiring([]byte("not json")) != nil {
		t.Fatal("a broken dump must mark nothing retiring")
	}
}
