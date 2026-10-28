package mcpx

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// memorySnapshotTools read and restore the history of claude's and codex's own memories
// (docs/log/39). Restoring is --write's.
func memorySnapshotTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_memory_snapshots",
			operator: func() map[string]any {
				return map[string]any{
					// docs/log/39 P4. export / import are deliberately not exposed: P3 put a secret
					// scan and the user's explicit ack in front of an export, and an MCP route where
					// the model acks and writes the file itself would be a second exit bypassing that
					// defence. Export and import stay a Console action the user performs.
					"name": "list_memory_snapshots",
					"description": "エージェントメモリ（claude の auto-memory / codex の memories）の変更履歴を新しい順に返す。各行は時刻・契機（auto/manual/pre-restore/restore/import）・変更されたプロジェクトを持つ。" +
						"「メモリがいつ変わったか」「おかしくなったのはいつからか」を聞かれた時や、restore_memory_snapshot で戻す時点を選ぶ前に呼ぶ。中身の差分は get_memory_snapshot を見る。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"limit": map[string]any{"type": "integer", "description": "返す件数（既定 20）"},
						},
					},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				var path string
				limit := a.Limit
				if limit <= 0 {
					limit = 20
				}
				// claude's and codex's history only, like get_memory_snapshot's diff: commits that touch
				// nothing but the AF memory are left out before the limit applies (ADR 0108).
				path = "/agents/memory/snapshots?native=1&limit=" + strconv.Itoa(limit)
				return mcpAgentGETRelay(req, p, path)
			},
		},
		{
			name: "get_memory_snapshot",
			operator: func() map[string]any {
				return map[string]any{
					"name": "get_memory_snapshot",
					"description": "指定時点のエージェントメモリの中身を返す。その時点に何が入っていたか（kind 別・プロジェクト別のファイル数）と、その snapshot が入れた変更の差分を返す。" +
						"rev（list_memory_snapshots の id）か at（日時。その時刻以前の直近 snapshot に解決）のどちらかを指定する。restore_memory_snapshot で戻す前に「戻したら何がどう変わるか」を確認するために呼ぶ。差分が大きい時は path で絞る。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"rev":  map[string]any{"type": "string", "description": "snapshot id（list_memory_snapshots の id）"},
							"at":   map[string]any{"type": "string", "description": "日時（RFC3339 等）。その時刻以前の直近 snapshot に解決する"},
							"path": map[string]any{"type": "string", "description": "差分を絞る repo 内パス（例: claude/projects/<slug>）。省略で全体"},
						},
					},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				// Return the tree (what was there at that point) and the diff (what that snapshot
				// changed) in one call. A restore scope can only be built from the tree: offering
				// the CURRENT roots as the choices makes an already-deleted project unselectable,
				// which defeats the main case, restoring memory that was deleted by mistake (why
				// docs/log/39 ③ added the tree).
				if a.Rev == "" && a.At == "" {
					return mcpToolErr(req.ID, "rev（snapshot id）か at（日時）のどちらかが必要です")
				}
				q := url.Values{}
				if a.Rev != "" {
					q.Set("rev", a.Rev)
				}
				if a.At != "" {
					q.Set("at", a.At)
				}
				tree, err := agentGET("/agents/memory/tree?" + q.Encode())
				if err != nil {
					return mcpToolErr(req.ID, "メモリの時点情報の取得に失敗しました: "+err.Error())
				}
				q.Del("rev")
				if a.Rev != "" {
					q.Set("to", a.Rev)
				}
				if a.Path != "" {
					q.Set("path", a.Path)
				}
				// claude's and codex's memories only: the AF memory's history (af/) is not this tool's,
				// and it is withheld from sessions and the assistant alike while its switch is off.
				q.Set("native", "1")
				diff, err := agentGET("/agents/memory/diff?" + q.Encode())
				if err != nil {
					return mcpToolErr(req.ID, "メモリの差分の取得に失敗しました: "+err.Error())
				}
				out, _ := json.Marshal(map[string]any{
					"tree": json.RawMessage(tree), "diff": json.RawMessage(diff),
				})
				return mcpTextResult(req.ID, string(out))
			},
		},
		{
			name: "restore_memory_snapshot",
			operator: func() map[string]any {
				return map[string]any{
					// Destructive but reversible: a pre-restore snapshot is always taken before the
					// restore is applied (docs/log/39 ④). Without "this can be undone" in the
					// description, the model either plays it too safe and refuses what the user asked
					// for, or takes it too lightly and skips the confirmation.
					"name": "restore_memory_snapshot",
					"description": "エージェントメモリを指定時点へ戻す。rev（list_memory_snapshots の id）か at（日時）で戻す先を指定し、範囲は all（全体）／projects（claude のプロジェクト単位）／kinds（claude・codex 単位）で指定する。範囲は必ず明示すること（省略は拒否される）。" +
						"履歴は書き換えず、戻す直前の状態も自動で snapshot に残るので、この操作自体を後から取り消せる。誤って消した／誤学習したメモリを戻す時に使う。" +
						"実行前に必ず get_memory_snapshot で戻り先の中身を確認し、『どの時点へ・どの範囲を戻すか』を利用者に示して承認を得ること。対象 kind のセッションが実行中だと結果の busy=true で返る。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"rev":      map[string]any{"type": "string", "description": "戻す先の snapshot id（list_memory_snapshots の id）"},
							"at":       map[string]any{"type": "string", "description": "戻す先の日時（RFC3339 等）。その時刻以前の直近 snapshot に解決する"},
							"all":      map[string]any{"type": "boolean", "description": "true で全体を戻す"},
							"projects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "戻す claude プロジェクトの slug（get_memory_snapshot の projects）"},
							"kinds":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "まるごと戻す kind（claude / codex）"},
						},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはメモリの復元を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.Rev == "" && a.At == "" {
					return mcpToolErr(req.ID, "rev（snapshot id）か at（日時）のどちらかが必要です")
				}
				// An omitted scope is NOT read as "everything". Killing that structurally at the
				// argument level stops the model dropping a field and rolling the whole memory back:
				// the user's approval was given for a specific scope, so widening it silently is a
				// betrayal of that approval.
				if !a.All && len(a.Kinds) == 0 && len(a.Projects) == 0 {
					return mcpToolErr(req.ID, "戻す範囲が必要です（all=true か projects / kinds を指定してください）")
				}
				reqBody, _ := json.Marshal(map[string]any{
					"rev": a.Rev, "at": a.At,
					"scope": map[string]any{"all": a.All, "kinds": a.Kinds, "projects": a.Projects},
				})
				out, err := AgentPOST("/agents/memory/restore", reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "メモリの復元に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
	}
}
