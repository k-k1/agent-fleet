package mcpx

import "encoding/json"

// assistantTools consult other assistants, under --write. Consults are advisory-only by
// construction (the sub-turn runs with no tools), so they can't loop or escalate.
func assistantTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_assistants",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_assistants",
					"description": "利用可能なアシスタント（常設ビルトイン＋ユーザー定義）の一覧を返す。ask_assistant で誰に相談するか選ぶ前に呼ぶ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは他アシスタントへの相談を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				out, err := agentGET("/assistants")
				if err != nil {
					return mcpToolErr(req.ID, "アシスタント一覧の取得に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{
			name: "ask_assistant",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "ask_assistant",
					"description": "別の専門アシスタントに助言を求める。相手は読み取り専用で1ターンだけ走り、助言テキストのみ返す（副作用なし・こちらの作業は代行しない）。例：SRE アシスタントにインシデント状況を見てもらう、ユーザー定義の専門アシスタントに設計を確認する。まず list_assistants で相手を選ぶこと。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"assistant": map[string]any{"type": "string", "description": "相談相手のアシスタント名または id"},
							"prompt":    map[string]any{"type": "string", "description": "相手に尋ねる内容（必要な文脈も含める）"},
						},
						"required": []string{"assistant", "prompt"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは他アシスタントへの相談を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.Assistant == "" || a.Prompt == "" {
					return mcpToolErr(req.ID, "assistant（相手）と prompt（相談内容）が必要です")
				}
				reqBody, _ := json.Marshal(map[string]string{"assistant": a.Assistant, "prompt": a.Prompt})
				out, err := AgentPOST("/chat/ask", reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "相談の実行に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
	}
}
