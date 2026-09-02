package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
)

// buildMux は Agent の全ルートを登録した mux を返す（docs/log/23 P0-2: main() からの
// 機械的抽出）。テストが実ルート表を httptest で叩けるようにするための分離で、登録
// 内容は main() にあったものと同一。httpx.RequireToken / httpx.LogRequests のラップは呼び出し側
// （main / テスト）の責務。
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	// Workspace 自身のリソース実測値（docs/log/63 §63.9）。CP がホストの cgroup を
	// 読めない構成（ECS 全般）で、メモリ / CPU / ディスクの唯一の出どころになる。
	mux.HandleFunc("GET /workspace/stats", handleWorkspaceStats)
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("GET /sessions/catalog", handleSessionCatalog)
	mux.HandleFunc("GET /notifications", handleNotifications)
	mux.HandleFunc("POST /notifications/ack", handleNotificationsAck)
	// Work items (docs/log/80): the CP posts the saved queries it owns and gets non-secret
	// rows back. Called by the CP itself (like /notifications), never by the Console,
	// so it needs no entry in the CP's agent-proxy allowlist.
	mux.HandleFunc("POST /work-items/fetch", handleWorkItemsFetch)
	// 唯一の書き戻し（docs/log/80 §80.10）。人が下書きを読んで押したときだけ CP 経由で来る。
	// MCP ツールは無い＝エージェントからは到達できない。
	mux.HandleFunc("POST /work-items/comment", handleWorkItemsComment)
	mux.HandleFunc("POST /sessions", handleCreateSession)
	// Idempotency reconcile (session_idempotency.go): resolve a create whose POST
	// response was lost to a client timeout, so the caller need not retry into a dup.
	// Top-level path (not under /sessions/{name}/…) to avoid a mux wildcard collision.
	mux.HandleFunc("GET /sessions-idempotency/{key}", handleIdempotencyLookup)
	mux.HandleFunc("GET /share-operations/{key}", handleShareOperationLookup)
	mux.HandleFunc("POST /sessions/{name}/fork", handleForkSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("POST /sessions/{name}/halt", handleHaltSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", handleRecreateSession)
	mux.HandleFunc("GET /sessions/archived", handleListArchived)
	mux.HandleFunc("GET /sessions/usage", handleSessionsUsage)
	// 機能別使用量の時系列（docs/log/46 P3 / ADR0029）。サーバ側で集計して返す。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /usage/series", handleUsageSeries)
	mux.HandleFunc("GET /sessions/cleanup", handleSessionsCleanup)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	// Cleanup archive (docs/log/32): the gz safety net for destructive tidy-up.
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	// 削除ロック（docs/log/45）: セッションを削除保護に固定/解除する。効くのは削除系
	// （/stop のメタ忘却・DELETE・TTL 自動 prune・作業コピー削除の巻き添え）だけで、
	// halt / archive は従来どおり通る。
	mux.HandleFunc("POST /sessions/{name}/lock", handleSessionLock)
	// 停止しないピン（docs/log/75）: アイドル自動停止からセッションと Workspace を
	// 期限付きで守る。shell / ssm の走行中ジョブを af 側から見分けられないことへの答え。
	mux.HandleFunc("POST /sessions/{name}/keep-awake", handleSessionKeepAwake)
	mux.HandleFunc("POST /sessions/{name}/archive", handleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", handleRestoreSession)
	// Programmatic drive I/O for the MCP tools (docs/0006 P3-6 E).
	mux.HandleFunc("POST /sessions/{name}/input", handleSessionInput)
	// Semantic turn ops + Interaction 応答（docs/log/27 P1.5/P2）— driver 抽象の受け口。
	// tui は tmux 経路へ委譲、managed は ThreadHandle へ（P2: opencode / P3: codex）。
	mux.Handle("POST /sessions/{name}/turn", withShareOperationIdempotency(http.HandlerFunc(handleSessionTurn)))
	mux.Handle("POST /sessions/{name}/respond", withShareOperationIdempotency(http.HandlerFunc(handleSessionRespond)))
	// オペレーターの AUQ 回答（docs/log/30）: 質問フォーム全体を choices（1-based）で
	// 一括回答。TUI claude はキー駆動、managed は Interaction 応答に落ちる。
	mux.Handle("POST /sessions/{name}/answer-question", withShareOperationIdempotency(http.HandlerFunc(handleSessionAnswerQuestion)))
	// 持ち越した対話への回答（docs/log/75）: 停止時に未応答だった質問/プラン/許可を、
	// 再開したうえで**文章として**配達する。キー列は 1 つも送らない。
	mux.HandleFunc("POST /sessions/{name}/carried-answer", handleSessionCarriedAnswer)
	// オペレーターのプラン承認/却下（docs/log/30）: approve=Enter、reject=中断＋feedback 送信。
	mux.Handle("POST /sessions/{name}/plan-respond", withShareOperationIdempotency(http.HandlerFunc(handleSessionPlanRespond)))
	// ThreadSettings の動的更新（docs/log/27 §9.4-3、managed 専用 — 稼働中セッションの
	// モデル/effort/モード変更）。tui は従来どおり /input のキー操作。
	mux.HandleFunc("GET /sessions/{name}/settings", handleSessionSettingsGet)
	mux.HandleFunc("POST /sessions/{name}/settings", handleSessionSettings)
	// ドライバ排他切替（docs/log/27 P3 §2: tui ⇄ managed、stop→drain→resume 経由）。
	mux.HandleFunc("POST /sessions/{name}/driver", handleSessionDriver)
	mux.HandleFunc("POST /sessions/{name}/paste-image", handlePasteImage)
	mux.HandleFunc("GET /sessions/{name}/pasted/{file}", handlePastedImage)
	// Memo image attachments (docs/log/21 画像添付) — membership-scoped, so keyed to the
	// container rather than a session (memo_paste.go). CP proxies /api/memos/* here.
	mux.HandleFunc("POST /memos/paste-image", handleMemoPasteImage)
	mux.HandleFunc("GET /memos/images/{file}", handleMemoPastedImage)
	mux.HandleFunc("POST /memos/images/gc", handleMemoImageGC)
	mux.HandleFunc("GET /sessions/{name}/status", handleSessionStatus)
	mux.HandleFunc("GET /sessions/{name}/output", handleSessionOutput)
	mux.HandleFunc("GET /sessions/{name}/ssm-login", handleSSMLoginStatus)
	mux.HandleFunc("POST /ssm/instances", handleSSMInstances)
	mux.HandleFunc("POST /sessions/{name}/start", handleStartSession)
	// Structured transcript (role + text + timestamp) for the Console chat view.
	mux.HandleFunc("GET /sessions/{name}/messages", handleSessionMessages)
	// Session-side MCP may propose the prompt for a follow-up session. Creation itself
	// remains a user action in the Console launch dialog.
	mux.HandleFunc("GET /sessions/{name}/handoff-proposal", handleSessionHandoffProposal)
	mux.HandleFunc("POST /sessions/{name}/handoff-proposal", handleSessionHandoffProposal)
	mux.HandleFunc("DELETE /sessions/{name}/handoff-proposal", handleSessionHandoffProposal)
	// メンバーへの引き継ぎ（docs/log/77）の座標。CP が offer を作る前にここへ聞く — 引き継ぎ先の
	// Workspace から所有者のディスクは見えないので、remote / branch / HEAD と「push 済みか」は
	// git に聞いた事実だけを載せる（モデルにも Console にも書かせない）。
	mux.HandleFunc("GET /sessions/{name}/handoff-context", handleSessionHandoffContext)
	// 転写のマーカー（docs/log/69 / ADR 0050）。所有者の Console と、CP 経由の共有先が読み書きする。
	mux.HandleFunc("GET /sessions/{name}/marks", handleSessionMarks)
	mux.HandleFunc("POST /sessions/{name}/marks", handleSessionMarks)
	mux.HandleFunc("DELETE /sessions/{name}/marks", handleSessionMarks)
	// Auto session-title suggestion (session_title.go): accept promotes it to Title,
	// dismiss discards it — either way it's never offered again for this session.
	mux.HandleFunc("POST /sessions/{name}/title/accept", handleAcceptSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/dismiss", handleDismissSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/suggest", handleSuggestTitle)
	mux.HandleFunc("POST /sessions/{name}/title/set", handleSetTitle)
	mux.HandleFunc("POST /sessions/{name}/suggest-branch", handleSessionSuggestBranch)
	mux.HandleFunc("POST /sessions/{name}/suggest-replies", handleSuggestReplies) // LLM 返信サジェスト v2（preview 専用）
	// ミラーのスキルピッカー（docs/log/50 / ADR0034）: セッションの worktree ＋ユーザーレベル
	// .claude/skills・commands を列挙する。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /sessions/{name}/skills", handleSessionSkills)
	// 変更ファイル帯の「コミット済み」判定（docs/log/68 P2）。転写側の一覧とは別に、
	// セッション開始以降のコミットに現れたパスだけを返す。
	mux.HandleFunc("GET /sessions/{name}/committed", handleSessionCommittedFiles)
	mux.HandleFunc("POST /sessions/{name}/rename-branch", handleSessionRenameBranch)
	mux.HandleFunc("GET /ws/pty", handlePTY)
	// Browser pane と外部所有 Chromium のアタッチ。**表は browserx 側が 1 つだけ持つ**
	// （internal/browserx/mux.go）—— ここに写しを置くと、片方に足したときもう片方の
	// テストが緑のまま通ってしまう。登録が落ちていないことは testdata/routes.golden が見る。
	browserx.RegisterRoutes(mux)

	// Assistant chat — headless-CLI LLM chat/translation, separate from tmux
	// sessions (docs/log/19). Non-streaming; the CP proxies these verbatim.
	mux.HandleFunc("GET /chat/conversations", chatx.HandleChatList)
	mux.HandleFunc("POST /chat/conversations", chatx.HandleChatCreate)
	mux.HandleFunc("GET /chat/conversations/{id}", chatx.HandleChatGet)
	mux.HandleFunc("PATCH /chat/conversations/{id}", chatx.HandleChatPatch) // 改名 / エージェント切替
	mux.HandleFunc("POST /chat/conversations/{id}/title/suggest", chatx.HandleChatSuggestTitle)
	mux.HandleFunc("POST /chat/conversations/{id}/suggest-replies", chatx.HandleChatSuggestReplies) // LLM 返信サジェスト v2（preview 専用）
	mux.HandleFunc("DELETE /chat/conversations/{id}", chatx.HandleChatDelete)
	mux.HandleFunc("POST /chat/conversations/{id}/lock", handleChatLock) // 削除ロック（docs/log/45）
	mux.HandleFunc("POST /chat/conversations/{id}/messages", chatx.HandleChatSend)
	mux.HandleFunc("POST /chat/conversations/{id}/stream", chatx.HandleChatStream)            // SSE (Phase B)
	mux.HandleFunc("POST /chat/conversations/{id}/stop", chatx.HandleChatStop)                // cancel a detached in-flight turn
	mux.HandleFunc("POST /chat/conversations/{id}/compact", chatx.HandleChatCompact)          // 要約引き継ぎ（docs/log/33 第2段）
	mux.HandleFunc("GET /chat/conversations/{id}/plan", chatx.HandleChatPlanGet)              // 作業計画の取得（docs/log/33 第5段・MCP 用の軽い口）
	mux.HandleFunc("PUT /chat/conversations/{id}/plan", chatx.HandleChatPlanSet)              // 作業計画の手編集（docs/log/33 第5段）
	mux.HandleFunc("POST /chat/conversations/{id}/plan/refresh", chatx.HandleChatPlanRefresh) // 作業計画の明示更新（同）
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", handleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", handleChatPastedImage)
	// Assistant-to-assistant consult (docs/log/19): af_write orchestrators' ask_assistant tool
	// hits this via the local stdio MCP. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/ask", chatx.HandleChatAsk)
	// スケジュール発のアシスタント発火（docs/log/38 session_mode=assistant）: CP スケジューラが
	// 会話（UUID/slug）へ 1 ターンを同期実行する。runOperatorTurn 委譲（assistant_turn.go）。
	mux.HandleFunc("POST /assistant-turns", handleAssistantTurn)
	// Session report kick (docs/log/30): the session-status hook / record-exit process posts
	// here when an operator-armed session reaches an awaiting-input / abnormal-exit
	// state. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/report", chatx.HandleChatReport)

	// Assistant templates — configurable chat personas (docs/log/19 Q2). Builtins are
	// code-injected; user-defined ones are stored under ~/.config/agent-fleet/assistants.
	mux.HandleFunc("GET /assistants", handleAssistantsList)
	mux.HandleFunc("POST /assistants", handleAssistantCreate)
	mux.HandleFunc("GET /assistants/{id}", handleAssistantGet)
	mux.HandleFunc("PUT /assistants/{id}", handleAssistantUpdate)
	mux.HandleFunc("DELETE /assistants/{id}", handleAssistantDelete)

	// Preview — reverse-proxy to a service the user started inside the container
	// (Spring Boot, dev server, ...). Reached only via the CP's /preview/{port}.
	mux.HandleFunc("/proxy/{port}/{rest...}", handlePreview)

	// Repository management — git ops on working copies under ~/repos.
	mux.HandleFunc("GET /repos", gitx.HandleListRepos)
	mux.HandleFunc("POST /repos", gitx.HandleCloneRepo)
	// 取り込み元が無いとき: ~/repos/<name> を作って git init するだけの新規作業コピー。
	// clone / svn checkout と違い同期（ネットワークを触らないのでジョブにする意味がない）。
	mux.HandleFunc("POST /repos/init", gitx.HandleInitRepo)
	// リポジトリ取り込みジョブ（docs/log/78）。clone / svn checkout は 202 でここへ移り、
	// 進捗と結末はこの一覧で観測する。DELETE は走行中なら中止、終端済みなら既読。
	mux.HandleFunc("GET /repo-jobs", handleListRepoJobs)
	mux.HandleFunc("DELETE /repo-jobs/{id}", handleDeleteRepoJob)
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	mux.HandleFunc("POST /repos/{name}/lock", handleRepoLock) // 削除ロック（docs/log/45）
	mux.HandleFunc("GET /repos/{name}/status", gitx.HandleRepoStatus)
	mux.HandleFunc("GET /repos/{name}/branches", gitx.HandleRepoBranches)
	mux.HandleFunc("DELETE /repos/{name}/branch", handleDeleteBranch) // ?branch=<name> (may contain "/")
	mux.HandleFunc("POST /repos/{name}/checkout", gitx.HandleRepoCheckout)
	mux.HandleFunc("POST /repos/{name}/fetch", gitx.HandleRepoFetch)
	mux.HandleFunc("POST /repos/{name}/ff", gitx.HandleRepoFF)
	mux.HandleFunc("POST /repos/{name}/parent-ff", gitx.HandleRepoParentFF)
	// Subversion (docs/log/41): checkout a URL (URL + basic auth), update to the latest
	// revision, and cleanup a wedged working-copy lock. Delete reuses DELETE /repos/{name}.
	mux.HandleFunc("POST /repos/svn", handleSvnCheckout)
	mux.HandleFunc("POST /repos/{name}/svn-update", handleSvnUpdate)
	mux.HandleFunc("POST /repos/{name}/svn-cleanup", handleSvnCleanup)
	// Launch prompt templates (repo 起動 modal): .claude/commands, .claude/skills,
	// .agent-fleet/launch-prompts.md — aggregated read-only from the working copy.
	mux.HandleFunc("GET /repos/{name}/prompt-templates", handleRepoPromptTemplates)
	// Project-scope MCP servers (docs/log/56 P0): read-only cross-file snapshot of the
	// working copy's own .mcp.json / opencode.json / .codex/config.toml / etc.
	// Separate axis from the MCP registry (docs/log/48) — never auto-triggered, never
	// touches user/global config.
	mux.HandleFunc("GET /repos/{name}/mcp", mcpx.HandleRepo)
	// docs/log/56 P1: reflect a server from one project MCP file to another
	// (plan → apply, optimistic lock via planHash) and add a git-ignore entry.
	mux.HandleFunc("POST /repos/{name}/mcp/plan", mcpx.HandleRepoPlan)
	mux.HandleFunc("POST /repos/{name}/mcp/apply", mcpx.HandleRepoApply)
	// Source-control view + light edits (docs/17 P3-5).
	mux.HandleFunc("GET /repos/{name}/changes", gitx.HandleRepoChanges)
	mux.HandleFunc("GET /repos/{name}/diff", gitx.HandleRepoDiff)
	mux.HandleFunc("GET /repos/{name}/log", gitx.HandleRepoLog)
	mux.HandleFunc("GET /repos/{name}/graph", gitx.HandleRepoGraph)
	mux.HandleFunc("GET /repos/{name}/submodules", gitx.HandleRepoSubmodules)
	mux.HandleFunc("GET /repos/{name}/show", gitx.HandleRepoShow)
	mux.HandleFunc("POST /repos/{name}/stage", gitx.HandleRepoStage)
	mux.HandleFunc("POST /repos/{name}/unstage", gitx.HandleRepoUnstage)
	mux.HandleFunc("POST /repos/{name}/discard", gitx.HandleRepoDiscard)
	mux.HandleFunc("POST /repos/{name}/commit", gitx.HandleRepoCommit)
	mux.HandleFunc("GET /repos/{name}/identity", gitx.HandleRepoIdentityGet)
	mux.HandleFunc("PUT /repos/{name}/identity", gitx.HandleRepoIdentityPut)
	mux.HandleFunc("GET /git/identity", gitx.HandleGlobalIdentityGet)
	mux.HandleFunc("PUT /git/identity", gitx.HandleGlobalIdentityPut)
	// File browser (docs/17 P3-5 段2 + FILES 改善): read tree/file, download raw,
	// upload into a dir, git-changes filter + viewer line marks.
	mux.HandleFunc("GET /fs/tree", handleFSTree)
	mux.HandleFunc("GET /fs/search", handleFSSearch)
	mux.HandleFunc("GET /fs/file", handleFSFile)
	mux.HandleFunc("PUT /fs/file", handleFSFilePut)
	// ミラー本文のパス参照解決（fs_resolve.go）。読むのは stat だけの read-only。
	mux.HandleFunc("POST /fs/resolve", handleFSResolve)
	// エディタの AI 変更提案（docs/log/44 Phase 4）。fs は触らない read-only 生成チャネル。
	mux.HandleFunc("POST /fs/suggest-edit", handleFSSuggestEdit)
	mux.HandleFunc("GET /fs/download", handleFSDownload)
	mux.HandleFunc("POST /fs/upload", handleFSUpload)
	mux.HandleFunc("GET /fs/changes", handleFSChanges)
	mux.HandleFunc("GET /fs/linemarks", handleFSLineMarks)
	mux.HandleFunc("POST /fs/mkdir", handleFSMkdir)
	mux.HandleFunc("POST /fs/newfile", handleFSNewFile)
	mux.HandleFunc("POST /fs/rename", handleFSRename)
	mux.HandleFunc("DELETE /fs/delete", handleFSDelete)

	// Claude settings (Remote Control / notifications / RTK hook) — Console toggles.
	mux.HandleFunc("GET /claude/settings", claude.HandleSettingsGet)
	mux.HandleFunc("PUT /claude/settings", claude.HandleSettingsPut)
	// Claude subscription usage (5-hour + weekly bars) for the WsBar chip.
	mux.HandleFunc("GET /claude/usage", claude.HandleUsage)
	mux.HandleFunc("GET /codex/usage", codex.HandleUsage)
	// Copilot account credit quota (remaining % + reset + plan) for the WsBar chip;
	// structured JSON from copilot_internal/user via the gh transparent-auth token.
	mux.HandleFunc("GET /copilot/usage", copilot.HandleUsage)
	mux.HandleFunc("GET /codex/settings", codex.HandleSettingsGet)
	mux.HandleFunc("PUT /codex/settings", codex.HandleSettingsPut)
	// codex / opencode rtk toggle (durable pref → on-disk artifacts) — Console.
	mux.HandleFunc("GET /agents/rtk", handleAgentRTKGet)
	mux.HandleFunc("PUT /agents/rtk", handleAgentRTKPut)
	// rtk token-savings history (rtk gain) for the WsBar "rtk 効果" chip.
	mux.HandleFunc("GET /agents/rtk/gain", handleAgentRTKGain)
	// ユーザー指示（docs/log/60）— フリート方針とプロジェクト指示の間の層。
	mux.HandleFunc("GET /user-notes", handleUserNotesGet)
	mux.HandleFunc("PUT /user-notes", handleUserNotesPut)
	mux.HandleFunc("GET /user-notes/preview", handleUserNotesPreview)
	// Live model catalogs (codex: `codex debug models` / opencode: `opencode models`)
	// for the Console's launch model picker.
	mux.HandleFunc("GET /agents/{kind}/models", handleAgentModels)
	// エージェントメモリの版管理（docs/log/39 / ADR 0022 P1〜P3）: claude/codex のメモリ md を
	// bare repo へ snapshot し、履歴・差分・指定時点への巻き戻し・環境間の移送を提供する。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /agents/memory/roots", handleMemoryRoots)
	mux.HandleFunc("GET /agents/memory/snapshots", handleMemorySnapshots)
	mux.HandleFunc("POST /agents/memory/snapshots", handleMemorySnapshotCreate)
	mux.HandleFunc("GET /agents/memory/diff", handleMemoryDiff)
	mux.HandleFunc("GET /agents/memory/tree", handleMemoryTree)
	mux.HandleFunc("POST /agents/memory/restore", handleMemoryRestore)
	mux.HandleFunc("PUT /agents/memory/settings", handleMemorySettings)
	// export は secret スキャン（★4）を通してから DL させる。import は独立系譜として
	// 受けてから、選んだ範囲だけを live へ適用する（P3）。
	mux.HandleFunc("GET /agents/memory/export", handleMemoryExport)
	mux.HandleFunc("POST /agents/memory/import", handleMemoryImport)
	mux.HandleFunc("POST /agents/memory/import/apply", handleMemoryImportApply)

	// Toolchain selection (node via nvm / java via pre-baked Temurin) — Console.
	mux.HandleFunc("GET /env/toolchains", handleToolchainsGet)
	mux.HandleFunc("PUT /env/toolchains", handleToolchainsPut)
	// On-demand Temurin install (jdk_install_http.go): the picker offers majors that
	// are not on disk yet, and this is the button that actually fetches one — the only
	// source of a JDK at all on ECS, where /usr/lib/jvm is empty.
	mux.HandleFunc("POST /env/jdk-install", handleJDKInstall)
	mux.HandleFunc("GET /env/jdk-install", handleJDKInstall)
	// バンドルツールの版レポート（実効 / 焼き込み / ~/.local override / ビルド時ピン）。
	mux.HandleFunc("GET /env/tool-versions", handleToolVersions)

	// Per-user UI preferences (Console display settings, synced across browsers).
	mux.HandleFunc("GET /env/ui-prefs", handleGetUIPrefs)
	mux.HandleFunc("PUT /env/ui-prefs", handlePutUIPrefs)

	// MCP レジストリ（docs/log/48 P0 / ADR0031）— ユーザー登録 MCP サーバーの CRUD と接続テスト。
	// テナント配布と組み込み連携は同じ一覧に読み取り専用で混ざる。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /mcp-servers", mcpx.HandleServersGet)
	mux.HandleFunc("POST /mcp-servers", mcpx.HandleServerCreate)
	mux.HandleFunc("POST /mcp-servers/test", mcpx.HandleServerTest)
	mux.HandleFunc("PUT /mcp-servers/{id}", mcpx.HandleServerUpdate)
	mux.HandleFunc("POST /mcp-servers/{id}/enabled", mcpx.HandleServerEnabled)
	mux.HandleFunc("DELETE /mcp-servers/{id}", mcpx.HandleServerDelete)
	// テナント配布（docs/log/48 P4）— 明示リフレッシュと、user_secret 定義の値入力。
	mux.HandleFunc("POST /mcp-servers/tenant-refresh", mcpx.HandleTenantRefresh)
	mux.HandleFunc("PUT /mcp-servers/{id}/secrets", mcpx.HandleServerSecrets)

	// Connections — per-user provider credentials (git tokens; Claude in Stage 3).
	mux.HandleFunc("GET /connections", handleConnectionsGet)
	mux.HandleFunc("GET /connections/git/{host}/repos", gitx.HandleListRemoteRepos)
	mux.HandleFunc("GET /connections/git/{host}/branches", gitx.HandleListRemoteBranches)
	mux.HandleFunc("PUT /connections/git/{host}", handlePutGitConn)
	mux.HandleFunc("PUT /connections/git/{host}/identity", gitx.HandleGitProviderIdentityPut)
	mux.HandleFunc("DELETE /connections/git/{host}", handleDeleteGitConn)
	// ★ No /connections/git/github/oauth/{start,poll} any more: both providers' OAuth
	// flows run in the Control Plane since docs/log/71, where the app can be read per
	// tenant. GitHub's token comes back through PUT /connections/git/github.com above.
	mux.HandleFunc("PUT /connections/git/bitbucket/oauth", gitx.HandleBitbucketStore)
	// Jira（docs/log/80 P1）: 作業項目の 2 つ目の取得元。site+email+token は保存前に
	// /rest/api/3/myself で検証する（3 項目あって打ち間違いが起きやすい）。
	mux.HandleFunc("PUT /connections/jira", handlePutJiraConn)
	mux.HandleFunc("DELETE /connections/jira", handleDeleteJiraConn)
	// OAuth（docs/log/80 §80.17）: /oauth は CP がコード交換のあとに叩く（Console は触らない）。
	// /site は Console → CP プロキシ —— 1 回の認可が複数サイトを含みうる。
	mux.HandleFunc("PUT /connections/jira/oauth", handleJiraOAuthStore)
	mux.HandleFunc("PUT /connections/jira/site", handlePutJiraSite)
	mux.HandleFunc("POST /connections/claude/start", claude.HandleStart)
	mux.HandleFunc("POST /connections/claude/complete", claude.HandleComplete)
	mux.HandleFunc("DELETE /connections/claude", claude.HandleDisconnect)
	mux.HandleFunc("PUT /connections/opencode", opencode.HandlePutConn)
	mux.HandleFunc("DELETE /connections/opencode/{env}", opencode.HandleDeleteConn)
	// opencode Console アカウント（device flow・docs/log/54）。APIキー方式と併存する。
	mux.HandleFunc("POST /connections/opencode/oauth/start", opencode.HandleOAuthStart)
	mux.HandleFunc("POST /connections/opencode/oauth/poll", opencode.HandleOAuthPoll)
	mux.HandleFunc("POST /connections/opencode/oauth/cancel", opencode.HandleOAuthCancel)
	mux.HandleFunc("DELETE /connections/opencode/oauth", opencode.HandleOAuthDisconnect)
	// 利用枠ページ（opencode.ai/workspace/{id}/go）への導線用の ID。手入力と、上限/残高
	// エラーからの自動学習の両方で埋まる（docs/log/54 §54.7）。
	mux.HandleFunc("PUT /connections/opencode/workspace", opencode.HandlePutWorkspace)
	// SVN saved basic-auth creds (docs/log/41): saved at checkout time; forget them here.
	mux.HandleFunc("DELETE /connections/svn", handleDeleteSvnConn)
	// agy quota gauge for the Console's AgyCard (docs/log/32 Track C — the Starter
	// Quota is an experimental pool, so the card always shows what's left).
	// The claude-style auth routes (start/complete/DELETE) land with Track A.
	mux.HandleFunc("GET /connections/agy/usage", agy.HandleUsage)
	mux.HandleFunc("POST /connections/codex/api-key", codex.HandleAPIKey)
	mux.HandleFunc("POST /connections/codex/device/start", codex.HandleDeviceStart)
	mux.HandleFunc("POST /connections/codex/device/poll", codex.HandleDevicePoll)
	mux.HandleFunc("DELETE /connections/codex", codex.HandleDisconnect)
	mux.HandleFunc("POST /connections/agy/start", agy.HandleStart)
	mux.HandleFunc("POST /connections/agy/complete", agy.HandleComplete)
	mux.HandleFunc("DELETE /connections/agy", agy.HandleDisconnect)
	// cursor login（docs/log/40 Track C）: 専用フロー型（claude/agy 同型）。ただしコード
	// 貼付は無く、ブラウザ承認をポーリングする（codex device-auth 型）ので start→poll。
	mux.HandleFunc("POST /connections/cursor/start", cursor.HandleStart)
	mux.HandleFunc("POST /connections/cursor/poll", cursor.HandlePoll)
	mux.HandleFunc("DELETE /connections/cursor", cursor.HandleDisconnect)
	// kiro login（docs/log/43 Track C）: device-flow 型（codex/cursor と同じ start→poll）。
	// kiro-cli 自身が AWS SSO をポーリングし、承認後に資格情報を書く。コード貼付は無く
	// URL+確認コードを表示するだけ。
	mux.HandleFunc("POST /connections/kiro/start", kiro.HandleStart)
	mux.HandleFunc("POST /connections/kiro/poll", kiro.HandlePoll)
	mux.HandleFunc("DELETE /connections/kiro", kiro.HandleDisconnect)
	// kiro on-demand install（docs/log/43 Track B/C）: lean イメージは ~855MB を焼かないため
	// 接続カードの「インストール」ボタンが叩く。POST で背景導入を開始、GET で進捗状態。
	mux.HandleFunc("POST /connections/kiro/install", handleKiroInstall)
	mux.HandleFunc("GET /connections/kiro/install", handleKiroInstall)
	mux.HandleFunc("PUT /connections/pagerduty", handlePutPagerDutyConn)
	mux.HandleFunc("DELETE /connections/pagerduty", handleDeletePagerDutyConn)
	mux.HandleFunc("PUT /connections/grafana", handlePutGrafanaConn)
	mux.HandleFunc("DELETE /connections/grafana", handleDeleteGrafanaConn)
	mux.HandleFunc("PUT /connections/cloudwatch", handlePutCloudWatchConn)
	mux.HandleFunc("DELETE /connections/cloudwatch", handleDeleteCloudWatchConn)
	mux.HandleFunc("PUT /connections/aws", handlePutAWSMCPConn)
	mux.HandleFunc("DELETE /connections/aws", handleDeleteAWSMCPConn)
	// チャットブリッジ（docs/log/37 P1）: Discord bot トークン＋宛先。inspect/guilds は
	// カードのセットアップウィザード（招待リンク生成→チャンネルピッカー）用。
	mux.HandleFunc("PUT /connections/discord", handlePutDiscordConn)
	mux.HandleFunc("DELETE /connections/discord", handleDeleteDiscordConn)
	mux.HandleFunc("POST /connections/discord/inspect", handleDiscordInspect)
	mux.HandleFunc("POST /connections/discord/guilds", handleDiscordGuilds)
	// チャットブリッジ Slack（docs/log/37 Slack 追随）: bot xoxb- ＋ app-level xapp- トークン＋宛先。
	// inspect/channels はセットアップウィザード（トークン検証→チャンネルピッカー＋email 解決）用。
	mux.HandleFunc("PUT /connections/slack", handlePutSlackConn)
	mux.HandleFunc("DELETE /connections/slack", handleDeleteSlackConn)
	mux.HandleFunc("POST /connections/slack/inspect", handleSlackInspect)
	mux.HandleFunc("POST /connections/slack/channels", handleSlackChannels)

	return mux
}
