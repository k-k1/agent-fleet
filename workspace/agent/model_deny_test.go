package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
)

// 除外判定の境界。claude は --model に別名でも完全 id でも渡せるので別名の除外は
// 完全 id にも効かなければならず、逆に opencode の課金経路違い（opencode/… と
// opencode-go/…）を巻き添えにしてはいけない。
func TestModelMatchesHiddenTokenBoundary(t *testing.T) {
	tests := []struct {
		requested, hidden string
		want              bool
	}{
		{"fable", "fable", true},
		{"Fable", "fable", true},          // 大小無視
		{"claude-fable-5", "fable", true}, // 別名の除外は完全 id にも効く
		{"claude-fable-5-20260101", "fable", true},
		{"opus", "fable", false},
		{"claude-opus-5", "fable", false},
		{"fablet", "fable", false}, // トークン境界（部分文字列では当てない）
		{"unfable", "fable", false},
		{"", "fable", false}, // 未指定＝CLI 既定に委ねる
		{"fable", "", false},
		// 具体 id（複数トークン）を隠しても、それを接頭辞に持つ別モデルは巻き添えに
		// しない。GPT-5.4 を隠したら mini まで消えた不具合の回帰。
		{"gpt-5.4-mini", "gpt-5.4", false},
		{"gpt-5.4", "gpt-5.4", true},
		{"gpt-5.4-mini", "gpt-5.4-mini", true},
		{"claude-fable-5-20260101", "claude-fable-5", false}, // 同上（別名でなく具体 id）
		// opencode: Zen を除外しても Go サブスクの同名は残る
		{"opencode-go/glm-5.2", "opencode/glm-5.2", false},
		{"opencode/glm-5.2", "opencode/glm-5.2", true},
		// 素の名前（glm-5.2）も複数トークンなので、もう族一致はしない。両経路を隠したい
		// なら両方の id を除外する（UI はどちらも一覧に出す）。取り過ぎない側に倒した。
		{"opencode/glm-5.2", "glm-5.2", false},
	}
	for _, tt := range tests {
		if got := modelMatchesHidden(tt.requested, tt.hidden); got != tt.want {
			t.Errorf("modelMatchesHidden(%q, %q) = %v, want %v", tt.requested, tt.hidden, got, tt.want)
		}
	}
}

func TestHiddenModelsForIgnoresJunkAndAllHiddenClaude(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"," ",42],"codex":"nope"}}`)
	if got := hiddenModelsFor("claude"); len(got) != 1 || got[0] != "fable" {
		t.Fatalf("hiddenModelsFor(claude) = %v, want [fable]", got)
	}
	if got := hiddenModelsFor("codex"); got != nil { // 型違いは「除外なし」
		t.Fatalf("hiddenModelsFor(codex) = %v, want nil", got)
	}
	if got := hiddenModelsFor("opencode"); got != nil { // 未設定
		t.Fatalf("hiddenModelsFor(opencode) = %v, want nil", got)
	}

	// claude を全ティア除外した設定は無視する（固定4ティアしか無く「既定」の選択肢も
	// 無いので、全部隠すと起動できるモデルが消える）。
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable","opus","sonnet","haiku"]}}`)
	if got := hiddenModelsFor("claude"); got != nil {
		t.Fatalf("all-hidden claude = %v, want nil (fail-safe)", got)
	}
	if modelHidden("claude", "fable") {
		t.Fatal("modelHidden(claude, fable) = true under the all-hidden fail-safe")
	}
}

// カタログ側（Console のピッカーと MCP list_models の合流点）から実際に消えること。
func TestAgentModelsHidesExcludedClaudeTier(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]}}`)
	req := httptest.NewRequest(http.MethodGet, "/agents/claude/models", nil)
	req.SetPathValue("kind", "claude")
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Models []agents.ModelChoice `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"opus", "sonnet", "haiku"}
	if len(got.Models) != len(want) {
		t.Fatalf("models = %+v, want ids %v", got.Models, want)
	}
	for i, id := range want {
		if got.Models[i].ID != id {
			t.Fatalf("models[%d].id = %q, want %q", i, got.Models[i].ID, id)
		}
	}
}

// 一覧から消すだけでは明示指定が素通りするので、起動ガードが本体（定時実行の
// モデル欄・MCP create_session・ユーザー定義アシスタントの自由入力が全部ここを通る）。
func TestCreateSessionRejectsHiddenModel(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]}}`)
	// ガードは副作用（clone / worktree / 起動）より前に立っているので、この呼び出しは
	// 何も生成しない。
	for _, model := range []string{"fable", "Fable", "claude-fable-5"} {
		req := httptest.NewRequest(http.MethodPost, "/sessions",
			strings.NewReader(`{"kind":"claude","model":"`+model+`"}`))
		rec := httptest.NewRecorder()
		handleCreateSession(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_hidden") {
			t.Fatalf("model %q: status = %d, body = %s, want 400 model_hidden", model, rec.Code, rec.Body.String())
		}
	}
	// 除外していないティアはこのガードで落ちない。
	if modelHidden("claude", "sonnet") {
		t.Fatal("modelHidden(claude, sonnet) = true, want false")
	}
}

// アシスタントの設定に除外モデルが残っていても採用しない（未設定扱いに落として
// 推奨／CLI 既定へ退避する）。
func TestAssistantModelPrefDropsHidden(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]},"assistantModels":{"claude":"fable"},"assistantAutoTurnModel":"fable"}`)
	if v, ok := assistantChatModelPref("claude"); ok {
		t.Fatalf("assistantChatModelPref = %q, %v; want dropped", v, ok)
	}
	if v := chatAutoTurnModel(); v != "" {
		t.Fatalf("chatAutoTurnModel = %q, want \"\"", v)
	}
	// 「推奨」番兵は実モデル id ではないので残る。
	writeUIPrefs(t, `{"hiddenModels":{"claude":["fable"]},"assistantModels":{"claude":"recommended"}}`)
	if v, ok := assistantChatModelPref("claude"); !ok || v != chatx.AssistantRecommendedModel {
		t.Fatalf("assistantChatModelPref = %q, %v; want recommended", v, ok)
	}
}
