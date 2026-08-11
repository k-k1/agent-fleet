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
)

// buildMux は Agent の全ルートを登録した mux を返す（docs/23 P0-2: main() からの
// 機械的抽出）。テストが実ルート表を httptest で叩けるようにするための分離で、登録
// 内容は main() にあったものと同一。httpx.RequireToken / httpx.LogRequests のラップは呼び出し側
// （main / テスト）の責務。
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("GET /notifications", handleNotifications)
	mux.HandleFunc("POST /notifications/ack", handleNotificationsAck)
	mux.HandleFunc("POST /sessions", handleCreateSession)
	// Idempotency reconcile (session_idempotency.go): resolve a create whose POST
	// response was lost to a client timeout, so the caller need not retry into a dup.
	// Top-level path (not under /sessions/{name}/…) to avoid a mux wildcard collision.
	mux.HandleFunc("GET /sessions-idempotency/{key}", handleIdempotencyLookup)
	mux.HandleFunc("POST /sessions/{name}/fork", handleForkSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("POST /sessions/{name}/halt", handleHaltSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", handleRecreateSession)
	mux.HandleFunc("GET /sessions/archived", handleListArchived)
	mux.HandleFunc("GET /sessions/usage", handleSessionsUsage)
	// 機能別使用量の時系列（docs/46 P3 / ADR0029）。サーバ側で集計して返す。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /usage/series", handleUsageSeries)
	mux.HandleFunc("GET /sessions/cleanup", handleSessionsCleanup)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	// Cleanup archive (docs/32): the gz safety net for destructive tidy-up.
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	// 削除ロック（docs/45）: セッションを削除保護に固定/解除する。効くのは削除系
	// （/stop のメタ忘却・DELETE・TTL 自動 prune・作業コピー削除の巻き添え）だけで、
	// halt / archive は従来どおり通る。
	mux.HandleFunc("POST /sessions/{name}/lock", handleSessionLock)
	mux.HandleFunc("POST /sessions/{name}/archive", handleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", handleRestoreSession)
	// Programmatic drive I/O for the MCP tools (docs/0006 P3-6 E).
	mux.HandleFunc("POST /sessions/{name}/input", handleSessionInput)
	// Semantic turn ops + Interaction 応答（docs/27 P1.5/P2）— driver 抽象の受け口。
	// tui は tmux 経路へ委譲、managed は ThreadHandle へ（P2: opencode / P3: codex）。
	mux.HandleFunc("POST /sessions/{name}/turn", handleSessionTurn)
	mux.HandleFunc("POST /sessions/{name}/respond", handleSessionRespond)
	// オペレーターの AUQ 回答（docs/30）: 質問フォーム全体を choices（1-based）で
	// 一括回答。TUI claude はキー駆動、managed は Interaction 応答に落ちる。
	mux.HandleFunc("POST /sessions/{name}/answer-question", handleSessionAnswerQuestion)
	// オペレーターのプラン承認/却下（docs/30）: approve=Enter、reject=中断＋feedback 送信。
	mux.HandleFunc("POST /sessions/{name}/plan-respond", handleSessionPlanRespond)
	// ThreadSettings の動的更新（docs/27 §9.4-3、managed 専用 — 稼働中セッションの
	// モデル/effort/モード変更）。tui は従来どおり /input のキー操作。
	mux.HandleFunc("GET /sessions/{name}/settings", handleSessionSettingsGet)
	mux.HandleFunc("POST /sessions/{name}/settings", handleSessionSettings)
	// ドライバ排他切替（docs/27 P3 §2: tui ⇄ managed、stop→drain→resume 経由）。
	mux.HandleFunc("POST /sessions/{name}/driver", handleSessionDriver)
	mux.HandleFunc("POST /sessions/{name}/paste-image", handlePasteImage)
	mux.HandleFunc("GET /sessions/{name}/pasted/{file}", handlePastedImage)
	// Memo image attachments (docs/21 画像添付) — membership-scoped, so keyed to the
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
	// Auto session-title suggestion (session_title.go): accept promotes it to Title,
	// dismiss discards it — either way it's never offered again for this session.
	mux.HandleFunc("POST /sessions/{name}/title/accept", handleAcceptSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/dismiss", handleDismissSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/suggest", handleSuggestTitle)
	mux.HandleFunc("POST /sessions/{name}/title/set", handleSetTitle)
	mux.HandleFunc("POST /sessions/{name}/suggest-branch", handleSessionSuggestBranch)
	mux.HandleFunc("POST /sessions/{name}/suggest-replies", handleSuggestReplies) // LLM 返信サジェスト v2（preview 専用）
	// ミラーのスキルピッカー（docs/50 / ADR0034）: セッションの worktree ＋ユーザーレベル
	// .claude/skills・commands を列挙する。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /sessions/{name}/skills", handleSessionSkills)
	mux.HandleFunc("POST /sessions/{name}/rename-branch", handleSessionRenameBranch)
	mux.HandleFunc("GET /ws/pty", handlePTY)
	// Browser pane — ephemeral BrowserContext + Page ownership and a restricted
	// screencast/input WebSocket. The CP proxies these internal routes verbatim.
	mux.HandleFunc("POST /browser/pages", handleBrowserPagesCreate)
	mux.HandleFunc("GET /browser/pages/{id}", handleBrowserPageGet)
	mux.HandleFunc("DELETE /browser/pages/{id}", handleBrowserPageDelete)
	mux.HandleFunc("GET /ws/browser", handleBrowserWebSocket)
	// External-owner Chromium attachments use a separate namespace and manager:
	// detach releases only AF's CDP session and never closes the target/process.
	mux.HandleFunc("GET /browser/attach-targets", handleBrowserAttachTargets)
	mux.HandleFunc("POST /browser/attachments", handleBrowserAttachmentCreate)
	mux.HandleFunc("GET /browser/attachments", handleBrowserAttachmentList)
	mux.HandleFunc("GET /browser/attachments/{id}", handleBrowserAttachmentGet)
	mux.HandleFunc("DELETE /browser/attachments/{id}", handleBrowserAttachmentDelete)
	mux.HandleFunc("POST /browser/attachments/{id}/control-mode", handleBrowserAttachmentControlMode)
	mux.HandleFunc("GET /browser/attachments/{id}/targets", handleBrowserAttachmentSiblingTargets)
	mux.HandleFunc("POST /browser/attachments/{id}/retarget", handleBrowserAttachmentRetarget)
	mux.HandleFunc("POST /browser/attachments/{id}/handoff", handleBrowserAttachmentHandoff)
	mux.HandleFunc("POST /browser/attachments/{id}/handoff-result", handleBrowserAttachmentHandoffResult)
	mux.HandleFunc("GET /ws/browser-attachments", handleBrowserAttachmentWebSocket)

	// Assistant chat — headless-CLI LLM chat/translation, separate from tmux
	// sessions (docs/19). Non-streaming; the CP proxies these verbatim.
	mux.HandleFunc("GET /chat/conversations", handleChatList)
	mux.HandleFunc("POST /chat/conversations", handleChatCreate)
	mux.HandleFunc("GET /chat/conversations/{id}", handleChatGet)
	mux.HandleFunc("PATCH /chat/conversations/{id}", handleChatPatch) // 改名 / エージェント切替
	mux.HandleFunc("POST /chat/conversations/{id}/title/suggest", handleChatSuggestTitle)
	mux.HandleFunc("POST /chat/conversations/{id}/suggest-replies", handleChatSuggestReplies) // LLM 返信サジェスト v2（preview 専用）
	mux.HandleFunc("DELETE /chat/conversations/{id}", handleChatDelete)
	mux.HandleFunc("POST /chat/conversations/{id}/lock", handleChatLock) // 削除ロック（docs/45）
	mux.HandleFunc("POST /chat/conversations/{id}/messages", handleChatSend)
	mux.HandleFunc("POST /chat/conversations/{id}/stream", handleChatStream)            // SSE (Phase B)
	mux.HandleFunc("POST /chat/conversations/{id}/stop", handleChatStop)                // cancel a detached in-flight turn
	mux.HandleFunc("POST /chat/conversations/{id}/compact", handleChatCompact)          // 要約引き継ぎ（docs/33 第2段）
	mux.HandleFunc("GET /chat/conversations/{id}/plan", handleChatPlanGet)              // 作業計画の取得（docs/33 第5段・MCP 用の軽い口）
	mux.HandleFunc("PUT /chat/conversations/{id}/plan", handleChatPlanSet)              // 作業計画の手編集（docs/33 第5段）
	mux.HandleFunc("POST /chat/conversations/{id}/plan/refresh", handleChatPlanRefresh) // 作業計画の明示更新（同）
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", handleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", handleChatPastedImage)
	// Assistant-to-assistant consult (docs/19): af_write orchestrators' ask_assistant tool
	// hits this via the local stdio MCP. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/ask", handleChatAsk)
	// スケジュール発のアシスタント発火（docs/38 session_mode=assistant）: CP スケジューラが
	// 会話（UUID/slug）へ 1 ターンを同期実行する。runOperatorTurn 委譲（assistant_turn.go）。
	mux.HandleFunc("POST /assistant-turns", handleAssistantTurn)
	// Session report kick (docs/30): the session-status hook / record-exit process posts
	// here when an operator-armed session reaches an awaiting-input / abnormal-exit
	// state. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/report", handleChatReport)

	// Assistant templates — configurable chat personas (docs/19 Q2). Builtins are
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
	mux.HandleFunc("GET /repos", handleListRepos)
	mux.HandleFunc("POST /repos", handleCloneRepo)
	mux.HandleFunc("DELETE /repos/{name}", handleDeleteRepo)
	mux.HandleFunc("POST /repos/{name}/lock", handleRepoLock) // 削除ロック（docs/45）
	mux.HandleFunc("GET /repos/{name}/status", handleRepoStatus)
	mux.HandleFunc("GET /repos/{name}/branches", handleRepoBranches)
	mux.HandleFunc("DELETE /repos/{name}/branch", handleDeleteBranch) // ?branch=<name> (may contain "/")
	mux.HandleFunc("POST /repos/{name}/checkout", handleRepoCheckout)
	mux.HandleFunc("POST /repos/{name}/fetch", handleRepoFetch)
	mux.HandleFunc("POST /repos/{name}/ff", handleRepoFF)
	mux.HandleFunc("POST /repos/{name}/parent-ff", handleRepoParentFF)
	// Subversion (docs/41): checkout a URL (URL + basic auth), update to the latest
	// revision, and cleanup a wedged working-copy lock. Delete reuses DELETE /repos/{name}.
	mux.HandleFunc("POST /repos/svn", handleSvnCheckout)
	mux.HandleFunc("POST /repos/{name}/svn-update", handleSvnUpdate)
	mux.HandleFunc("POST /repos/{name}/svn-cleanup", handleSvnCleanup)
	// Launch prompt templates (repo 起動 modal): .claude/commands, .claude/skills,
	// .agent-fleet/launch-prompts.md — aggregated read-only from the working copy.
	mux.HandleFunc("GET /repos/{name}/prompt-templates", handleRepoPromptTemplates)
	// Project-scope MCP servers (docs/56 P0): read-only cross-file snapshot of the
	// working copy's own .mcp.json / opencode.json / .codex/config.toml / etc.
	// Separate axis from the MCP registry (docs/48) — never auto-triggered, never
	// touches user/global config.
	mux.HandleFunc("GET /repos/{name}/mcp", handleRepoMCP)
	// docs/56 P1: reflect a server from one project MCP file to another
	// (plan → apply, optimistic lock via planHash) and add a git-ignore entry.
	mux.HandleFunc("POST /repos/{name}/mcp/plan", handleRepoMCPPlan)
	mux.HandleFunc("POST /repos/{name}/mcp/apply", handleRepoMCPApply)
	// Source-control view + light edits (docs/17 P3-5).
	mux.HandleFunc("GET /repos/{name}/changes", handleRepoChanges)
	mux.HandleFunc("GET /repos/{name}/diff", handleRepoDiff)
	mux.HandleFunc("GET /repos/{name}/log", handleRepoLog)
	mux.HandleFunc("GET /repos/{name}/graph", handleRepoGraph)
	mux.HandleFunc("GET /repos/{name}/submodules", handleRepoSubmodules)
	mux.HandleFunc("GET /repos/{name}/show", handleRepoShow)
	mux.HandleFunc("POST /repos/{name}/stage", handleRepoStage)
	mux.HandleFunc("POST /repos/{name}/unstage", handleRepoUnstage)
	mux.HandleFunc("POST /repos/{name}/discard", handleRepoDiscard)
	mux.HandleFunc("POST /repos/{name}/commit", handleRepoCommit)
	mux.HandleFunc("GET /repos/{name}/identity", handleRepoIdentityGet)
	mux.HandleFunc("PUT /repos/{name}/identity", handleRepoIdentityPut)
	mux.HandleFunc("GET /git/identity", handleGlobalIdentityGet)
	mux.HandleFunc("PUT /git/identity", handleGlobalIdentityPut)
	// File browser (docs/17 P3-5 段2 + FILES 改善): read tree/file, download raw,
	// upload into a dir, git-changes filter + viewer line marks.
	mux.HandleFunc("GET /fs/tree", handleFSTree)
	mux.HandleFunc("GET /fs/search", handleFSSearch)
	mux.HandleFunc("GET /fs/file", handleFSFile)
	mux.HandleFunc("PUT /fs/file", handleFSFilePut)
	// エディタの AI 変更提案（docs/44 Phase 4）。fs は触らない read-only 生成チャネル。
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
	// Live model catalogs (codex: `codex debug models` / opencode: `opencode models`)
	// for the Console's launch model picker.
	mux.HandleFunc("GET /agents/{kind}/models", handleAgentModels)
	// エージェントメモリの版管理（docs/39 / ADR 0022 P1〜P3）: claude/codex のメモリ md を
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
	// バンドルツールの版レポート（実効 / 焼き込み / ~/.local override / ビルド時ピン）。
	mux.HandleFunc("GET /env/tool-versions", handleToolVersions)

	// Per-user UI preferences (Console display settings, synced across browsers).
	mux.HandleFunc("GET /env/ui-prefs", handleGetUIPrefs)
	mux.HandleFunc("PUT /env/ui-prefs", handlePutUIPrefs)

	// MCP レジストリ（docs/48 P0 / ADR0031）— ユーザー登録 MCP サーバーの CRUD と接続テスト。
	// テナント配布と組み込み連携は同じ一覧に読み取り専用で混ざる。
	// ⚠️ control-plane/routes.go にも同じパスの登録が要る（CP は明示許可リスト方式）。
	mux.HandleFunc("GET /mcp-servers", handleMCPServersGet)
	mux.HandleFunc("POST /mcp-servers", handleMCPServerCreate)
	mux.HandleFunc("POST /mcp-servers/test", handleMCPServerTest)
	mux.HandleFunc("PUT /mcp-servers/{id}", handleMCPServerUpdate)
	mux.HandleFunc("POST /mcp-servers/{id}/enabled", handleMCPServerEnabled)
	mux.HandleFunc("DELETE /mcp-servers/{id}", handleMCPServerDelete)
	// テナント配布（docs/48 P4）— 明示リフレッシュと、user_secret 定義の値入力。
	mux.HandleFunc("POST /mcp-servers/tenant-refresh", handleMCPTenantRefresh)
	mux.HandleFunc("PUT /mcp-servers/{id}/secrets", handleMCPServerSecrets)

	// Connections — per-user provider credentials (git tokens; Claude in Stage 3).
	mux.HandleFunc("GET /connections", handleConnectionsGet)
	mux.HandleFunc("GET /connections/git/{host}/repos", handleListRemoteRepos)
	mux.HandleFunc("GET /connections/git/{host}/branches", handleListRemoteBranches)
	mux.HandleFunc("PUT /connections/git/{host}", handlePutGitConn)
	mux.HandleFunc("PUT /connections/git/{host}/identity", handleGitProviderIdentityPut)
	mux.HandleFunc("DELETE /connections/git/{host}", handleDeleteGitConn)
	mux.HandleFunc("POST /connections/git/github/oauth/start", handleGithubOAuthStart)
	mux.HandleFunc("POST /connections/git/github/oauth/poll", handleGithubOAuthPoll)
	mux.HandleFunc("PUT /connections/git/bitbucket/oauth", handleBitbucketStore)
	mux.HandleFunc("POST /connections/claude/start", claude.HandleStart)
	mux.HandleFunc("POST /connections/claude/complete", claude.HandleComplete)
	mux.HandleFunc("DELETE /connections/claude", claude.HandleDisconnect)
	mux.HandleFunc("PUT /connections/opencode", opencode.HandlePutConn)
	mux.HandleFunc("DELETE /connections/opencode/{env}", opencode.HandleDeleteConn)
	// opencode Console アカウント（device flow・docs/54）。APIキー方式と併存する。
	mux.HandleFunc("POST /connections/opencode/oauth/start", opencode.HandleOAuthStart)
	mux.HandleFunc("POST /connections/opencode/oauth/poll", opencode.HandleOAuthPoll)
	mux.HandleFunc("POST /connections/opencode/oauth/cancel", opencode.HandleOAuthCancel)
	mux.HandleFunc("DELETE /connections/opencode/oauth", opencode.HandleOAuthDisconnect)
	// 利用枠ページ（opencode.ai/workspace/{id}/go）への導線用の ID。手入力と、上限/残高
	// エラーからの自動学習の両方で埋まる（docs/54 §54.7）。
	mux.HandleFunc("PUT /connections/opencode/workspace", opencode.HandlePutWorkspace)
	// SVN saved basic-auth creds (docs/41): saved at checkout time; forget them here.
	mux.HandleFunc("DELETE /connections/svn", handleDeleteSvnConn)
	// agy quota gauge for the Console's AgyCard (docs/32 Track C — the Starter
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
	// cursor login（docs/40 Track C）: 専用フロー型（claude/agy 同型）。ただしコード
	// 貼付は無く、ブラウザ承認をポーリングする（codex device-auth 型）ので start→poll。
	mux.HandleFunc("POST /connections/cursor/start", cursor.HandleStart)
	mux.HandleFunc("POST /connections/cursor/poll", cursor.HandlePoll)
	mux.HandleFunc("DELETE /connections/cursor", cursor.HandleDisconnect)
	// kiro login（docs/43 Track C）: device-flow 型（codex/cursor と同じ start→poll）。
	// kiro-cli 自身が AWS SSO をポーリングし、承認後に資格情報を書く。コード貼付は無く
	// URL+確認コードを表示するだけ。
	mux.HandleFunc("POST /connections/kiro/start", kiro.HandleStart)
	mux.HandleFunc("POST /connections/kiro/poll", kiro.HandlePoll)
	mux.HandleFunc("DELETE /connections/kiro", kiro.HandleDisconnect)
	// kiro on-demand install（docs/43 Track B/C）: lean イメージは ~855MB を焼かないため
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
	// チャットブリッジ（docs/37 P1）: Discord bot トークン＋宛先。inspect/guilds は
	// カードのセットアップウィザード（招待リンク生成→チャンネルピッカー）用。
	mux.HandleFunc("PUT /connections/discord", handlePutDiscordConn)
	mux.HandleFunc("DELETE /connections/discord", handleDeleteDiscordConn)
	mux.HandleFunc("POST /connections/discord/inspect", handleDiscordInspect)
	mux.HandleFunc("POST /connections/discord/guilds", handleDiscordGuilds)
	// チャットブリッジ Slack（docs/37 Slack 追随）: bot xoxb- ＋ app-level xapp- トークン＋宛先。
	// inspect/channels はセットアップウィザード（トークン検証→チャンネルピッカー＋email 解決）用。
	mux.HandleFunc("PUT /connections/slack", handlePutSlackConn)
	mux.HandleFunc("DELETE /connections/slack", handleDeleteSlackConn)
	mux.HandleFunc("POST /connections/slack/inspect", handleSlackInspect)
	mux.HandleFunc("POST /connections/slack/channels", handleSlackChannels)

	return mux
}
