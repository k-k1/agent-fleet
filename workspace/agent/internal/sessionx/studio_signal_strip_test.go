package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The studio's signal line (ADR 0100 decision 5) is stripped from every prompt that reads the
// member's words back: reply suggestions, the automatic title and the branch name. A turn that
// was nothing but the signal leaves the window entirely.
func TestStudioSignalLeavesTheSuggestionPrompts(t *testing.T) {
	const signal = "[studio v4 · 下書きが変わった · 新しい結果 2 → get_image_studio]"
	turns := []transcript.Turn{
		{Role: "user", Text: "背景を夜にして\n" + signal},
		{Role: "assistant", Text: "cfg を 5 に下げました。"},
		{Role: "user", Text: signal},
	}
	for name, got := range map[string]string{
		"reply":  ReplySuggestPrompt(turns, "ja"),
		"title":  titleSuggestPrompt(turns, "ja"),
		"branch": BranchSuggestPrompt(turns),
	} {
		if strings.Contains(got, "[studio ") {
			t.Errorf("%s prompt kept the studio signal:\n%s", name, got)
		}
		if !strings.Contains(got, "user: 背景を夜にして\n") {
			t.Errorf("%s prompt lost the member's own words:\n%s", name, got)
		}
		if strings.Contains(got, "user: \n") {
			t.Errorf("%s prompt kept a turn that was only the signal:\n%s", name, got)
		}
	}
}
