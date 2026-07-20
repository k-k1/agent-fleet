package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
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
	mux.HandleFunc("POST /sessions/{name}/fork", handleForkSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("POST /sessions/{name}/halt", handleHaltSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", handleRecreateSession)
	mux.HandleFunc("GET /sessions/archived", handleListArchived)
	mux.HandleFunc("GET /sessions/usage", handleSessionsUsage)
	mux.HandleFunc("POST /sessions/{name}/archive", handleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", handleRestoreSession)
	// Programmatic drive I/O for the MCP tools (docs/0006 P3-6 E).
	mux.HandleFunc("POST /sessions/{name}/input", handleSessionInput)
	// Semantic turn ops + Interaction 応答（docs/27 P1.5/P2）— driver 抽象の受け口。
	// tui は tmux 経路へ委譲、managed は ThreadHandle へ（P2: opencode / P3: codex）。
	mux.HandleFunc("POST /sessions/{name}/turn", handleSessionTurn)
	mux.HandleFunc("POST /sessions/{name}/respond", handleSessionRespond)
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
	// Auto session-title suggestion (session_title.go): accept promotes it to Title,
	// dismiss discards it — either way it's never offered again for this session.
	mux.HandleFunc("POST /sessions/{name}/title/accept", handleAcceptSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/dismiss", handleDismissSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/suggest", handleSuggestTitle)
	mux.HandleFunc("POST /sessions/{name}/title/set", handleSetTitle)
	mux.HandleFunc("POST /sessions/{name}/suggest-branch", handleSessionSuggestBranch)
	mux.HandleFunc("POST /sessions/{name}/rename-branch", handleSessionRenameBranch)
	mux.HandleFunc("GET /ws/pty", handlePTY)
	// Browser pane — ephemeral BrowserContext + Page ownership and a restricted
	// screencast/input WebSocket. The CP proxies these internal routes verbatim.
	mux.HandleFunc("POST /browser/pages", handleBrowserPagesCreate)
	mux.HandleFunc("GET /browser/pages/{id}", handleBrowserPageGet)
	mux.HandleFunc("DELETE /browser/pages/{id}", handleBrowserPageDelete)
	mux.HandleFunc("GET /ws/browser", handleBrowserWebSocket)

	// Assistant chat — headless-CLI LLM chat/translation, separate from tmux
	// sessions (docs/19). Non-streaming; the CP proxies these verbatim.
	mux.HandleFunc("GET /chat/conversations", handleChatList)
	mux.HandleFunc("POST /chat/conversations", handleChatCreate)
	mux.HandleFunc("GET /chat/conversations/{id}", handleChatGet)
	mux.HandleFunc("PATCH /chat/conversations/{id}", handleChatRename)
	mux.HandleFunc("POST /chat/conversations/{id}/title/suggest", handleChatSuggestTitle)
	mux.HandleFunc("DELETE /chat/conversations/{id}", handleChatDelete)
	mux.HandleFunc("POST /chat/conversations/{id}/messages", handleChatSend)
	mux.HandleFunc("POST /chat/conversations/{id}/stream", handleChatStream) // SSE (Phase B)
	mux.HandleFunc("POST /chat/conversations/{id}/stop", handleChatStop)     // cancel a detached in-flight turn
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", handleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", handleChatPastedImage)
	// Assistant-to-assistant consult (docs/19): af_write orchestrators' ask_assistant tool
	// hits this via the local stdio MCP. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/ask", handleChatAsk)
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
	mux.HandleFunc("GET /repos/{name}/status", handleRepoStatus)
	mux.HandleFunc("GET /repos/{name}/branches", handleRepoBranches)
	mux.HandleFunc("POST /repos/{name}/checkout", handleRepoCheckout)
	mux.HandleFunc("POST /repos/{name}/fetch", handleRepoFetch)
	mux.HandleFunc("POST /repos/{name}/ff", handleRepoFF)
	// Launch prompt templates (repo 起動 modal): .claude/commands, .claude/skills,
	// .agent-fleet/launch-prompts.md — aggregated read-only from the working copy.
	mux.HandleFunc("GET /repos/{name}/prompt-templates", handleRepoPromptTemplates)
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

	// Toolchain selection (node via nvm / java via pre-baked Temurin) — Console.
	mux.HandleFunc("GET /env/toolchains", handleToolchainsGet)
	mux.HandleFunc("PUT /env/toolchains", handleToolchainsPut)
	// バンドルツールの版レポート（実効 / 焼き込み / ~/.local override / ビルド時ピン）。
	mux.HandleFunc("GET /env/tool-versions", handleToolVersions)

	// Per-user UI preferences (Console display settings, synced across browsers).
	mux.HandleFunc("GET /env/ui-prefs", handleGetUIPrefs)
	mux.HandleFunc("PUT /env/ui-prefs", handlePutUIPrefs)

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
	mux.HandleFunc("PUT /connections/pagerduty", handlePutPagerDutyConn)
	mux.HandleFunc("DELETE /connections/pagerduty", handleDeletePagerDutyConn)
	mux.HandleFunc("PUT /connections/grafana", handlePutGrafanaConn)
	mux.HandleFunc("DELETE /connections/grafana", handleDeleteGrafanaConn)
	mux.HandleFunc("PUT /connections/cloudwatch", handlePutCloudWatchConn)
	mux.HandleFunc("DELETE /connections/cloudwatch", handleDeleteCloudWatchConn)

	return mux
}
