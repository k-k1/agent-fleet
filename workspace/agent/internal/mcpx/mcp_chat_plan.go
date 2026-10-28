package mcpx

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// chatPlanTools are the work plan pinned to the operator's own conversation (docs/log/33
// stage 5, option D), under --write.
func chatPlanTools() []toolSpec {
	refuse := func() string {
		if !writeEnabled() {
			return "このアシスタントは書き込みツールを許可されていません"
		}
		return ""
	}
	return []toolSpec{
		{name: "get_chat_plan", operator: func() map[string]any {
			return map[string]any{
				"name": "get_chat_plan",
				"description": "この会話に固定されている作業計画（docs/log/33 第5段）を返す。作業計画は要約を通さず**原文のまま**新しいセッションへ毎回引き継がれる枠で、" +
					"コンテキスト圧縮で会話の記憶が畳まれても消えない。利用者が Console 側で計画を書き換えていることがあるので、" +
					"長い作業の再開時や、計画に沿っているか確かめたいときに読むこと（会話履歴を読み直すより安い）。",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}
		}, operatorWrite: true, refuse: refuse, handle: mcpChatPlan},
		{name: "set_chat_plan", operator: func() map[string]any {
			return map[string]any{
				"name": "set_chat_plan",
				"description": "この会話の作業計画（docs/log/33 第5段）を書き換える。**全文置換**なので、まず get_chat_plan で現在の計画を読み、それを土台に変更点だけを反映した全文を渡すこと。" +
					"ここに書いた内容は要約されず、原文のまま以降の新しいセッションへ毎回引き継がれる — つまり『圧縮されても絶対に忘れない場所』。" +
					"利用者と壁打ちして段取り・担当・順序が決まった直後や、レーンが1つ終わって次の波に進んだときに更新する。" +
					"書き方は次の3見出しで、**完了した作業を網羅列挙しないこと**（git や課題管理システムを見れば分かることは書かない）:\n" +
					"## 制約（環境・禁止事項・運用ルールなど、この先ずっと効く前提）\n" +
					"## 前提（次の一手に必要な既成事実だけ。ID・ブランチ名・意図的な例外など）\n" +
					"## これからやること（順序・依存・分岐条件）\n" +
					"判断基準は「完了したか」ではなく『これが無いと次の一手を間違えるか』。計画を空にはできない（不要になったら利用者が Console の作業計画パネルから消す）。",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"plan": map[string]any{"type": "string", "description": "置き換え後の作業計画の全文（Markdown）。"},
					},
					"required": []string{"plan"},
				},
			}
		}, operatorWrite: true, refuse: refuse, handle: mcpChatPlan},
	}
}

// mcpChatPlan reads or replaces the plan. The target is ALWAYS this server's own conversation
// (convID()) and there is no conversation-id argument — the same convention as create_schedule's
// owner_conv override, making "an operator only writes to itself" a property of the wiring.
//
// The read is under the --write gate too: both tools are advertised only in the write set, so
// this follows the existing rule that the advertised set is the scope boundary (af_report,
// docs/log/19 Q2).
func mcpChatPlan(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
	if convID() == "" {
		return mcpToolErr(req.ID, "この経路には会話が結び付いていないため、作業計画は扱えません")
	}
	path := "/chat/conversations/" + url.PathEscape(convID()) + "/plan"
	if p.Name == "get_chat_plan" {
		out, err := agentGET(path)
		if err != nil {
			return mcpToolErr(req.ID, "作業計画の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	}
	// Never let an empty value clear it: discarding the plan is the user's decision, made
	// in the Console's work-plan panel. A whole-text replacement that is empty or only
	// whitespace is usually an accident (a failed summary, truncated output).
	if strings.TrimSpace(a.Plan) == "" {
		return mcpToolErr(req.ID, "plan（作業計画の全文）が空です。計画を消したい場合は利用者に Console の作業計画パネルから操作してもらってください")
	}
	// notice=true: this is the only route that moves the plan while the user is not
	// watching, so leave a card in the conversation.
	body, _ := json.Marshal(map[string]any{"plan": a.Plan, "notice": true})
	if _, err := agentDo(http.MethodPut, path, body); err != nil {
		return mcpToolErr(req.ID, "作業計画の更新に失敗しました: "+err.Error())
	}
	// Do not return the whole conversation: that would feed its full text back to the
	// model on every plan write.
	return mcpTextResult(req.ID, "作業計画を更新しました（以降の新しいセッションへ原文のまま引き継がれます）。")
}
