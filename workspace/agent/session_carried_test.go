package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// 配達文面は飾りではなく機能。docs/75 §75.10 C の実測で、「質問し直さず」の一文が
// 無いと claude は質問し直すことが分かっている（会話木から質問が消えているため）。
// 文面を書き換えるときは、この 2 つの不変条件を壊していないか見ること。
func TestCarriedQuestionPromptCarriesQuestionAndForbidsReasking(t *testing.T) {
	qs := []transcript.Question{{Header: "書き込む語", Question: "out.txt に書き込む語を選んでください。"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"みかん"}}})
	if !strings.Contains(got, "質問し直さず") {
		t.Errorf("配達文に「質問し直さず」が無い: %q", got)
	}
	if !strings.Contains(got, "out.txt に書き込む語を選んでください。") {
		t.Errorf("質問文そのものを運んでいない（回答だけ送っても何への回答か分からない）: %q", got)
	}
	if !strings.Contains(got, "みかん") {
		t.Errorf("選ばれたラベルが無い: %q", got)
	}
}

// TUI へは send-keys -l で 1 バイトずつ載るので、改行はそのまま Enter として作用する
// （docs/dev/92）。配達文は必ず 1 行。
func TestCarriedPromptsAreSingleLine(t *testing.T) {
	qs := []transcript.Question{{Question: "改行\nを含む\r\n質問"}, {Question: "2 問目"}}
	answers := []CarriedAnswer{{Labels: []string{"A\nB"}, Notes: "補足\nの続き"}, {Labels: []string{"C"}}}
	for _, s := range []string{
		buildCarriedQuestionPrompt(qs, answers),
		buildCarriedPlanPrompt(true, "気を\nつけて"),
		buildCarriedPlanPrompt(false, "作り\n直して"),
		buildCarriedPermissionPrompt("Bash · rm\n-rf"),
	} {
		if strings.ContainsAny(s, "\n\r\t") {
			t.Errorf("配達文に制御文字が残っている: %q", s)
		}
		if s == "" {
			t.Error("配達文が空")
		}
	}
}

func TestCarriedQuestionPromptMultiSelectAndNotes(t *testing.T) {
	qs := []transcript.Question{{Question: "好きな果物は？"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"りんご", "みかん"}, Notes: "皮ごと"}})
	if !strings.Contains(got, "りんご・みかん") {
		t.Errorf("複数選択が 1 つに畳まれていない: %q", got)
	}
	if !strings.Contains(got, "（補足: 皮ごと）") {
		t.Errorf("自由入力が落ちている: %q", got)
	}
	// 選択肢を 1 つも選ばずに自由入力だけ、も成立する（preview 付き AUQ の notes 形）。
	only := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Notes: "どちらでもない"}})
	if !strings.Contains(only, "どちらでもない") {
		t.Errorf("自由入力のみの回答が落ちた: %q", only)
	}
	if buildCarriedQuestionPrompt(qs, []CarriedAnswer{{}}) != "" {
		t.Error("空の回答からは配達文を作らない")
	}
}

// ★承認は「承認」ではなく実行の指示（§75.10 E）。文面が承認と却下で取り違えられないこと。
func TestCarriedPlanPromptApproveRejectAreDistinct(t *testing.T) {
	ok := buildCarriedPlanPrompt(true, "")
	ng := buildCarriedPlanPrompt(false, "")
	if !strings.Contains(ok, "承認します") || strings.Contains(ok, "承認しません") {
		t.Errorf("承認の文面が承認になっていない: %q", ok)
	}
	if !strings.Contains(ng, "承認しません") {
		t.Errorf("却下の文面が却下になっていない: %q", ng)
	}
	if withFb := buildCarriedPlanPrompt(false, "手順 2 を分けて"); !strings.Contains(withFb, "手順 2 を分けて") {
		t.Errorf("却下時の指示が落ちている: %q", withFb)
	}
}

// 昇格は pending-* を持ち越しへ移すだけ。優先順位は question > plan > permission
// （EffectiveModal と同じ）。
func TestPromoteCarriedPrefersQuestionOverPlan(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-1"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"どっち？","options":[{"label":"A"}]}]`))
	status.WritePendingPlan(sid, "# 計画\n- やる")
	status.WritePendingPermission(sid, "Bash · ls")
	status.AppendPendingText(sid, "その前置き")

	if !status.PromoteCarried(sid, "halt") {
		t.Fatal("PromoteCarried = false, want true")
	}
	c, ok := status.ReadCarried(sid)
	if !ok {
		t.Fatal("持ち越しが読めない")
	}
	if c.Kind != "question" {
		t.Errorf("Kind = %q, want question（question は plan/permission に優先する）", c.Kind)
	}
	if !strings.Contains(string(c.Questions), "どっち？") {
		t.Errorf("質問ペイロードが運ばれていない: %s", c.Questions)
	}
	if c.Text != "その前置き" {
		t.Errorf("Text = %q, want その前置き（カードの文脈）", c.Text)
	}
	if c.Reason != "halt" {
		t.Errorf("Reason = %q, want halt", c.Reason)
	}
}

func TestPromoteCarriedPlanOnlyAndNoopWhenNothingPending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-2"
	if status.PromoteCarried(sid, "stopped") {
		t.Error("保留が何も無いのに昇格した")
	}
	status.WritePendingPlan(sid, "# 計画")
	if !status.PromoteCarried(sid, "stopped") {
		t.Fatal("plan の昇格が false")
	}
	c, _ := status.ReadCarried(sid)
	if c.Kind != "plan" || c.Plan != "# 計画" {
		t.Errorf("plan が運ばれていない: %+v", c)
	}
	// ★上書きしない: halt で昇格した後、再開の boot フックがもう一度呼ぶ経路がある。
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"後から"}]`))
	if status.PromoteCarried(sid, "stopped") {
		t.Error("既存の持ち越しを上書きした（halt→resume の順で消える）")
	}
	if c2, _ := status.ReadCarried(sid); c2.Kind != "plan" {
		t.Errorf("Kind = %q, want plan のまま", c2.Kind)
	}
}

// status.Remove（halt が呼ぶ）は持ち越しを消してはいけない — 消す順序を間違えると
// 昇格した直後に自分で捨てることになる。
func TestStatusRemoveKeepsCarried(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-3"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"残る？"}]`))
	status.PromoteCarried(sid, "halt")
	status.Remove(sid)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Error("pending-question が残っている")
	}
	if _, ok := status.ReadCarried(sid); !ok {
		t.Error("status.Remove が持ち越しまで消した")
	}
	status.RemoveCarried(sid)
	if _, ok := status.ReadCarried(sid); ok {
		t.Error("RemoveCarried の後も残っている")
	}
}

// 保留（生きたモーダル）があるあいだは持ち越しを出さない — 出すと Console に
// 「キーで答えるカード」と「文章で答えるカード」が同時に並ぶ。
func TestSurfaceCarriedYieldsToLivePending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-4"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"いま出ている"}]`))
	status.PromoteCarried(sid, "halt")

	resp := map[string]any{}
	surfacePendingPayloads(resp, sid, "question")
	surfaceCarried(resp, sid)
	if _, ok := resp["carried"]; ok {
		t.Error("保留中なのに持ち越しまで出した")
	}

	// 保留が消えた後（＝再開後）は持ち越しだけが出る。
	status.RemovePendingQuestion(sid)
	resp2 := map[string]any{}
	surfacePendingPayloads(resp2, sid, "idle")
	surfaceCarried(resp2, sid)
	c, ok := resp2["carried"].(map[string]any)
	if !ok {
		t.Fatal("持ち越しが出ていない")
	}
	if c["kind"] != "question" {
		t.Errorf("kind = %v, want question", c["kind"])
	}
}
