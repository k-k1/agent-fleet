package agents

import (
	"strings"
	"testing"
)

func labels(list []ModelChoice) string {
	var out []string
	for _, m := range list {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// Ordered by label (the string the user actually sees), so mixed case still reads
// alphabetically; the id only breaks ties, so the order never shuffles between calls.
func TestSortByLabelIsCaseInsensitiveAndTotal(t *testing.T) {
	got := labels(SortByLabel([]ModelChoice{
		{ID: "b", Label: "GPT-5.6-Sol"},
		{ID: "a2", Label: "claude"},
		{ID: "a1", Label: "claude"},
		{ID: "c", Label: "Claude Opus"},
	}))
	// claude(a1) < claude(a2) < "Claude Opus"(c) < "GPT-5.6-Sol"(b), shown by id.
	if want := "a1,a2,c,b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Group rank wins first (opencode puts Go models on top); within a rank the order is by
// label, not by input order — falling back to input order would leak each source's own
// ordering straight through to the user.
func TestSortGroupedRanksThenLabels(t *testing.T) {
	rank := func(m ModelChoice) int {
		if strings.HasPrefix(m.ID, "go/") {
			return 0
		}
		return 1
	}
	in := []ModelChoice{
		{ID: "zen/kimi", Label: "zen/kimi"},
		{ID: "go/qwen", Label: "go/qwen"},
		{ID: "zen/aya", Label: "zen/aya"},
		{ID: "go/glm", Label: "go/glm"},
	}
	if got, want := labels(SortGrouped(in, rank)), "go/glm,go/qwen,zen/aya,zen/kimi"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Reordering the input changes nothing: that is the point — the order must not depend on
	// the source.
	shuffled := []ModelChoice{in[3], in[0], in[2], in[1]}
	if got, want := labels(SortGrouped(shuffled, rank)), "go/glm,go/qwen,zen/aya,zen/kimi"; got != want {
		t.Errorf("shuffled: got %q, want %q", got, want)
	}
}
