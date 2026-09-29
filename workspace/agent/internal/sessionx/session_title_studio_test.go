package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const studioPersonaJA = "このセッションでは、Agent Fleet の画像生成スタジオ「ホーム · Claude Code」で、利用者と一緒にプロンプトの下書きを作ります。\n" +
	"- 利用者の発言を受けたら、答える前にまず get_image_studio を呼んでください。"

// Every studio session opens with the persona and the agent's greeting to it; left in the
// title window they named every studio session "prompt drafting in the image studio".
func TestTitlePromptSkipsTheStudioPersona(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: studioPersonaJA},
		{Role: "assistant", Text: "こんにちは、画像スタジオ「ホーム · Claude Code」です。どんな絵を作りたいか教えてください。"},
		{Role: "user", Text: "夕暮れの港にいる毛づくろいをする三毛猫を描きたい"},
		{Role: "assistant", Text: "下書きを入れました。"},
	}
	for name, prompt := range map[string]string{
		"title":  titleSuggestPrompt(turns, "ja"),
		"branch": BranchSuggestPrompt(turns),
	} {
		if strings.Contains(prompt, "get_image_studio") || strings.Contains(prompt, "どんな絵を作りたいか") {
			t.Errorf("%s prompt still carries the persona exchange:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "三毛猫") {
			t.Errorf("%s prompt lost the member's request:\n%s", name, prompt)
		}
	}
	if got := withoutStudioPersona(turns[:2]); len(got) != 0 {
		t.Errorf("persona and greeting alone must leave nothing to title, got %d turns", len(got))
	}
}

func TestWithoutStudioPersonaLeavesOtherSessionsAlone(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "ログイン画面のリダイレクトを直して"},
		{Role: "assistant", Text: "直しました。"},
	}
	if got := withoutStudioPersona(turns); len(got) != 2 {
		t.Errorf("a non-studio session lost turns: %d", len(got))
	}
}
