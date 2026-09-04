package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
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

// buildMux returns a mux with every Agent route registered (docs/log/23 P0-2). Keeping it
// out of main() is what lets a test drive the real route table through httptest. Wrapping
// it in httpx.RequireToken / httpx.LogRequests is the caller's job (main / the test).
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	// The Workspace's own measured resources (docs/log/63 §63.9). Where the CP cannot read
	// the host's cgroup (ECS in general), this is the only source of memory / CPU / disk.
	mux.HandleFunc("GET /workspace/stats", handleWorkspaceStats)
	mux.HandleFunc("GET /sessions", sessionx.HandleListSessions)
	mux.HandleFunc("GET /sessions/catalog", sessionx.HandleSessionCatalog)
	mux.HandleFunc("GET /notifications", handleNotifications)
	mux.HandleFunc("POST /notifications/ack", handleNotificationsAck)
	// Work items (docs/log/80): the CP posts the saved queries it owns and gets non-secret
	// rows back. Called by the CP itself (like /notifications), never by the Console,
	// so it needs no entry in the CP's agent-proxy allowlist.
	mux.HandleFunc("POST /work-items/fetch", handleWorkItemsFetch)
	// The only write-back (docs/log/80 §80.10). It arrives via the CP only once a human has
	// read the draft and pressed the button. There is no MCP tool, so an agent cannot reach it.
	mux.HandleFunc("POST /work-items/comment", handleWorkItemsComment)
	mux.HandleFunc("POST /sessions", sessionx.HandleCreateSession)
	// Idempotency reconcile (session_idempotency.go): resolve a create whose POST
	// response was lost to a client timeout, so the caller need not retry into a dup.
	// Top-level path (not under /sessions/{name}/…) to avoid a mux wildcard collision.
	mux.HandleFunc("GET /sessions-idempotency/{key}", sessionx.HandleIdempotencyLookup)
	mux.HandleFunc("GET /share-operations/{key}", handleShareOperationLookup)
	mux.HandleFunc("POST /sessions/{name}/fork", sessionx.HandleForkSession)
	mux.HandleFunc("POST /sessions/{name}/stop", sessionx.HandleStopSession)
	mux.HandleFunc("POST /sessions/{name}/halt", sessionx.HandleHaltSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", sessionx.HandleRecreateSession)
	mux.HandleFunc("GET /sessions/archived", sessionx.HandleListArchived)
	mux.HandleFunc("GET /sessions/usage", sessionx.HandleSessionsUsage)
	// Per-feature usage time series (docs/log/46 P3 / ADR0029), aggregated server-side.
	// control-plane/routes.go needs the same path registered: the CP is an explicit allowlist.
	mux.HandleFunc("GET /usage/series", handleUsageSeries)
	mux.HandleFunc("GET /sessions/cleanup", sessionx.HandleSessionsCleanup)
	mux.HandleFunc("DELETE /sessions/{name}", handleDeleteSession)
	// Cleanup archive (docs/log/32): the gz safety net for destructive tidy-up.
	mux.HandleFunc("GET /cleanup/archives", handleListCleanupArchives)
	mux.HandleFunc("POST /cleanup/archives/{id}/restore", handleRestoreCleanupArchive)
	mux.HandleFunc("DELETE /cleanup/archives/{id}", handlePurgeCleanupArchive)
	// Deletion lock (docs/log/45): pin a session to delete-protected, or release it. It bites
	// on deletion only (/stop forgetting the metadata, DELETE, the TTL auto-prune, collateral
	// from deleting a working copy); halt / archive still go through.
	mux.HandleFunc("POST /sessions/{name}/lock", sessionx.HandleSessionLock)
	// Keep-awake pin (docs/log/75): shields the session and the Workspace from idle auto-stop
	// for a bounded time. The answer to af being unable to tell whether a shell / ssm job is
	// still running.
	mux.HandleFunc("POST /sessions/{name}/keep-awake", sessionx.HandleSessionKeepAwake)
	mux.HandleFunc("POST /sessions/{name}/archive", sessionx.HandleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", sessionx.HandleRestoreSession)
	// Programmatic drive I/O for the MCP tools (docs/0006 P3-6 E).
	mux.HandleFunc("POST /sessions/{name}/input", sessionx.HandleSessionInput)
	// Semantic turn ops + Interaction reply (docs/log/27 P1.5/P2) — the entry point of the
	// driver abstraction. tui delegates to the tmux path, managed to a ThreadHandle
	// (P2: opencode / P3: codex).
	mux.Handle("POST /sessions/{name}/turn", withShareOperationIdempotency(http.HandlerFunc(sessionx.HandleSessionTurn)))
	mux.Handle("POST /sessions/{name}/respond", withShareOperationIdempotency(http.HandlerFunc(sessionx.HandleSessionRespond)))
	// The operator's AUQ answer (docs/log/30): answers a whole question form at once as
	// choices (1-based). TUI claude is key-driven, managed falls through to an Interaction
	// reply.
	mux.Handle("POST /sessions/{name}/answer-question", withShareOperationIdempotency(http.HandlerFunc(sessionx.HandleSessionAnswerQuestion)))
	// Answer to a carried-over interaction (docs/log/75): a question, plan or permission left
	// unanswered at stop time is delivered as text after resuming. Not one key sequence is sent.
	mux.HandleFunc("POST /sessions/{name}/carried-answer", sessionx.HandleSessionCarriedAnswer)
	// The operator's plan approval / rejection (docs/log/30): approve = Enter, reject =
	// interrupt plus sending feedback.
	mux.Handle("POST /sessions/{name}/plan-respond", withShareOperationIdempotency(http.HandlerFunc(sessionx.HandleSessionPlanRespond)))
	// Live ThreadSettings update (docs/log/27 §9.4-3, managed only — model / effort / mode
	// changes on a running session). tui does it with key input on /input.
	mux.HandleFunc("GET /sessions/{name}/settings", sessionx.HandleSessionSettingsGet)
	mux.HandleFunc("POST /sessions/{name}/settings", sessionx.HandleSessionSettings)
	// Exclusive driver switch (docs/log/27 P3 §2: tui ⇄ managed, via stop -> drain -> resume).
	mux.HandleFunc("POST /sessions/{name}/driver", sessionx.HandleSessionDriver)
	mux.HandleFunc("POST /sessions/{name}/paste-image", sessionx.HandlePasteImage)
	mux.HandleFunc("GET /sessions/{name}/pasted/{file}", sessionx.HandlePastedImage)
	// Memo image attachments (docs/log/21 画像添付) — membership-scoped, so keyed to the
	// container rather than a session (memo_paste.go). CP proxies /api/memos/* here.
	mux.HandleFunc("POST /memos/paste-image", handleMemoPasteImage)
	mux.HandleFunc("GET /memos/images/{file}", handleMemoPastedImage)
	mux.HandleFunc("POST /memos/images/gc", handleMemoImageGC)
	mux.HandleFunc("GET /sessions/{name}/status", sessionx.HandleSessionStatus)
	mux.HandleFunc("GET /sessions/{name}/output", sessionx.HandleSessionOutput)
	mux.HandleFunc("GET /sessions/{name}/ssm-login", sessionx.HandleSSMLoginStatus)
	mux.HandleFunc("POST /ssm/instances", handleSSMInstances)
	mux.HandleFunc("POST /sessions/{name}/start", sessionx.HandleStartSession)
	// Structured transcript (role + text + timestamp) for the Console chat view.
	mux.HandleFunc("GET /sessions/{name}/messages", sessionx.HandleSessionMessages)
	// Session-side MCP may propose the prompt for a follow-up session. Creation itself
	// remains a user action in the Console launch dialog.
	mux.HandleFunc("GET /sessions/{name}/handoff-proposal", sessionx.HandleSessionHandoffProposal)
	mux.HandleFunc("POST /sessions/{name}/handoff-proposal", sessionx.HandleSessionHandoffProposal)
	mux.HandleFunc("DELETE /sessions/{name}/handoff-proposal", sessionx.HandleSessionHandoffProposal)
	// Coordinates for a handoff to another member (docs/log/77). The CP asks here before it
	// creates the offer: the recipient's Workspace cannot see the owner's disk, so remote /
	// branch / HEAD and "is it pushed" carry only facts read from git — never anything the
	// model or the Console wrote.
	mux.HandleFunc("GET /sessions/{name}/handoff-context", sessionx.HandleSessionHandoffContext)
	// Transcript marks (docs/log/69 / ADR 0050). Read and written by the owner's Console and,
	// through the CP, by a share recipient.
	mux.HandleFunc("GET /sessions/{name}/marks", sessionx.HandleSessionMarks)
	mux.HandleFunc("POST /sessions/{name}/marks", sessionx.HandleSessionMarks)
	mux.HandleFunc("DELETE /sessions/{name}/marks", sessionx.HandleSessionMarks)
	// Auto session-title suggestion (session_title.go): accept promotes it to Title,
	// dismiss discards it — either way it's never offered again for this session.
	mux.HandleFunc("POST /sessions/{name}/title/accept", sessionx.HandleAcceptSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/dismiss", sessionx.HandleDismissSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/suggest", sessionx.HandleSuggestTitle)
	mux.HandleFunc("POST /sessions/{name}/title/set", sessionx.HandleSetTitle)
	mux.HandleFunc("POST /sessions/{name}/suggest-branch", sessionx.HandleSessionSuggestBranch)
	mux.HandleFunc("POST /sessions/{name}/suggest-replies", sessionx.HandleSuggestReplies) // LLM reply suggestion v2 (preview only)
	// Mirror skill picker (docs/log/50 / ADR0034): lists the session's worktree plus the
	// user-level .claude/skills and commands.
	// control-plane/routes.go needs the same path registered: the CP is an explicit allowlist.
	mux.HandleFunc("GET /sessions/{name}/skills", sessionx.HandleSessionSkills)
	// The "committed" verdict for the changed-files bar (docs/log/68 P2). Separate from the
	// transcript's own list: it returns only paths that appeared in a commit made since the
	// session started.
	mux.HandleFunc("GET /sessions/{name}/committed", sessionx.HandleSessionCommittedFiles)
	mux.HandleFunc("POST /sessions/{name}/rename-branch", sessionx.HandleSessionRenameBranch)
	mux.HandleFunc("GET /ws/pty", handlePTY)
	// Browser pane plus attaching an externally owned Chromium. The table lives in exactly one
	// place, browserx (internal/browserx/mux.go): a copy here would let an addition to one
	// side leave the other side's test green. testdata/routes.golden watches that nothing
	// falls out of the registration.
	browserx.RegisterRoutes(mux)

	// Assistant chat — headless-CLI LLM chat/translation, separate from tmux
	// sessions (docs/log/19). Non-streaming; the CP proxies these verbatim.
	mux.HandleFunc("GET /chat/conversations", chatx.HandleChatList)
	mux.HandleFunc("POST /chat/conversations", chatx.HandleChatCreate)
	mux.HandleFunc("GET /chat/conversations/{id}", chatx.HandleChatGet)
	mux.HandleFunc("PATCH /chat/conversations/{id}", chatx.HandleChatPatch) // rename / switch agent
	mux.HandleFunc("POST /chat/conversations/{id}/title/suggest", chatx.HandleChatSuggestTitle)
	mux.HandleFunc("POST /chat/conversations/{id}/suggest-replies", chatx.HandleChatSuggestReplies) // LLM reply suggestion v2 (preview only)
	mux.HandleFunc("DELETE /chat/conversations/{id}", chatx.HandleChatDelete)
	mux.HandleFunc("POST /chat/conversations/{id}/lock", sessionx.HandleChatLock) // deletion lock (docs/log/45)
	mux.HandleFunc("POST /chat/conversations/{id}/messages", chatx.HandleChatSend)
	mux.HandleFunc("POST /chat/conversations/{id}/stream", chatx.HandleChatStream)            // SSE (Phase B)
	mux.HandleFunc("POST /chat/conversations/{id}/stop", chatx.HandleChatStop)                // cancel a detached in-flight turn
	mux.HandleFunc("POST /chat/conversations/{id}/compact", chatx.HandleChatCompact)          // summary carry-forward (docs/log/33 stage 2)
	mux.HandleFunc("GET /chat/conversations/{id}/plan", chatx.HandleChatPlanGet)              // read the work plan (docs/log/33 stage 5; the light face for MCP)
	mux.HandleFunc("PUT /chat/conversations/{id}/plan", chatx.HandleChatPlanSet)              // hand-edit the work plan (docs/log/33 stage 5)
	mux.HandleFunc("POST /chat/conversations/{id}/plan/refresh", chatx.HandleChatPlanRefresh) // explicit work-plan refresh (same)
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", sessionx.HandleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", sessionx.HandleChatPastedImage)
	// Assistant-to-assistant consult (docs/log/19): af_write orchestrators' ask_assistant tool
	// hits this via the local stdio MCP. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/ask", chatx.HandleChatAsk)
	// Assistant turn fired by a schedule (docs/log/38 session_mode=assistant): the CP
	// scheduler runs one turn synchronously against a conversation (UUID/slug), delegating
	// to runOperatorTurn (assistant_turn.go).
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
	// New working copy with no import source: mkdir ~/repos/<name> and git init, nothing more.
	// Unlike clone / svn checkout it is synchronous — it touches no network, so making it a
	// job would buy nothing.
	mux.HandleFunc("POST /repos/init", gitx.HandleInitRepo)
	// Repository import jobs (docs/log/78). clone / svn checkout answer 202 and move here;
	// progress and outcome are observed through this list. DELETE aborts a running job and
	// marks a finished one as read.
	mux.HandleFunc("GET /repo-jobs", handleListRepoJobs)
	mux.HandleFunc("DELETE /repo-jobs/{id}", handleDeleteRepoJob)
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	mux.HandleFunc("POST /repos/{name}/lock", sessionx.HandleRepoLock) // deletion lock (docs/log/45)
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
	// Launch prompt templates (repo launch modal): .claude/commands, .claude/skills,
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
	// File browser (docs/17 P3-5 stage 2 + the FILES improvements): read tree/file, download raw,
	// upload into a dir, git-changes filter + viewer line marks.
	mux.HandleFunc("GET /fs/tree", handleFSTree)
	mux.HandleFunc("GET /fs/search", handleFSSearch)
	mux.HandleFunc("GET /fs/file", handleFSFile)
	mux.HandleFunc("PUT /fs/file", handleFSFilePut)
	// Resolves path references in mirror text (fs_resolve.go) — read-only, stat only.
	mux.HandleFunc("POST /fs/resolve", handleFSResolve)
	// The editor's AI edit suggestion (docs/log/44 Phase 4) — a read-only generation channel
	// that never touches the fs.
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
	// User instructions (docs/log/60) — the layer between fleet policy and project instructions.
	mux.HandleFunc("GET /user-notes", handleUserNotesGet)
	mux.HandleFunc("PUT /user-notes", handleUserNotesPut)
	mux.HandleFunc("GET /user-notes/preview", handleUserNotesPreview)
	// Live model catalogs (codex: `codex debug models` / opencode: `opencode models`)
	// for the Console's launch model picker.
	mux.HandleFunc("GET /agents/{kind}/models", handleAgentModels)
	// Agent memory version control (docs/log/39 / ADR 0022 P1-P3): snapshots claude/codex
	// memory md into a bare repo, giving history, diffs, rollback to a chosen point and
	// transfer between environments.
	// control-plane/routes.go needs the same path registered: the CP is an explicit allowlist.
	mux.HandleFunc("GET /agents/memory/roots", memoryx.HandleMemoryRoots)
	mux.HandleFunc("GET /agents/memory/snapshots", memoryx.HandleMemorySnapshots)
	mux.HandleFunc("POST /agents/memory/snapshots", memoryx.HandleMemorySnapshotCreate)
	mux.HandleFunc("GET /agents/memory/diff", memoryx.HandleMemoryDiff)
	mux.HandleFunc("GET /agents/memory/tree", memoryx.HandleMemoryTree)
	mux.HandleFunc("POST /agents/memory/restore", memoryx.HandleMemoryRestore)
	mux.HandleFunc("PUT /agents/memory/settings", memoryx.HandleMemorySettings)
	// export only lets a download through after the secret scan (★4). import is taken in as
	// an independent lineage first, and only the selected range is then applied to live (P3).
	mux.HandleFunc("GET /agents/memory/export", memoryx.HandleMemoryExport)
	mux.HandleFunc("POST /agents/memory/import", memoryx.HandleMemoryImport)
	mux.HandleFunc("POST /agents/memory/import/apply", memoryx.HandleMemoryImportApply)

	// Toolchain selection (node via nvm / java via pre-baked Temurin) — Console.
	mux.HandleFunc("GET /env/toolchains", handleToolchainsGet)
	mux.HandleFunc("PUT /env/toolchains", handleToolchainsPut)
	// On-demand Temurin install (jdk_install_http.go): the picker offers majors that
	// are not on disk yet, and this is the button that actually fetches one — the only
	// source of a JDK at all on ECS, where /usr/lib/jvm is empty.
	mux.HandleFunc("POST /env/jdk-install", handleJDKInstall)
	mux.HandleFunc("GET /env/jdk-install", handleJDKInstall)
	// The same for node: without this, selecting a version that is not installed silently
	// does nothing, exactly the gap the JDK had (docs/decisions/0068, and the head of
	// node_install.go).
	mux.HandleFunc("POST /env/node-install", handleNodeInstall)
	mux.HandleFunc("GET /env/node-install", handleNodeInstall)
	// Version report for the bundled tools (effective / baked in / ~/.local override /
	// build-time pin).
	mux.HandleFunc("GET /env/tool-versions", handleToolVersions)

	// Per-user UI preferences (Console display settings, synced across browsers).
	mux.HandleFunc("GET /env/ui-prefs", handleGetUIPrefs)
	mux.HandleFunc("PUT /env/ui-prefs", handlePutUIPrefs)

	// MCP registry (docs/log/48 P0 / ADR0031) — CRUD and a connection test for user-registered
	// MCP servers. Tenant distribution and built-in integrations blend into the same list,
	// read-only.
	// control-plane/routes.go needs the same path registered: the CP is an explicit allowlist.
	mux.HandleFunc("GET /mcp-servers", mcpx.HandleServersGet)
	mux.HandleFunc("POST /mcp-servers", mcpx.HandleServerCreate)
	mux.HandleFunc("POST /mcp-servers/test", mcpx.HandleServerTest)
	mux.HandleFunc("PUT /mcp-servers/{id}", mcpx.HandleServerUpdate)
	mux.HandleFunc("POST /mcp-servers/{id}/enabled", mcpx.HandleServerEnabled)
	mux.HandleFunc("DELETE /mcp-servers/{id}", mcpx.HandleServerDelete)
	// Tenant distribution (docs/log/48 P4) — an explicit refresh, and filling in the values a
	// user_secret definition declares.
	mux.HandleFunc("POST /mcp-servers/tenant-refresh", mcpx.HandleTenantRefresh)
	mux.HandleFunc("PUT /mcp-servers/{id}/secrets", mcpx.HandleServerSecrets)

	// Connections — per-user provider credentials (git tokens; Claude in Stage 3).
	mux.HandleFunc("GET /connections", handleConnectionsGet)
	mux.HandleFunc("GET /connections/git/{host}/repos", gitx.HandleListRemoteRepos)
	mux.HandleFunc("GET /connections/git/{host}/branches", gitx.HandleListRemoteBranches)
	mux.HandleFunc("PUT /connections/git/{host}", handlePutGitConn)
	mux.HandleFunc("PUT /connections/git/{host}/identity", gitx.HandleGitProviderIdentityPut)
	mux.HandleFunc("DELETE /connections/git/{host}", handleDeleteGitConn)
	// There is no /connections/git/github/oauth/{start,poll}: both providers' OAuth flows
	// run in the Control Plane (docs/log/71), where the app can be read per tenant.
	// GitHub's token comes back through PUT /connections/git/github.com above.
	mux.HandleFunc("PUT /connections/git/bitbucket/oauth", gitx.HandleBitbucketStore)
	// Jira (docs/log/80 P1): the second source of work items. site+email+token is verified
	// against /rest/api/3/myself before it is stored — three fields is plenty for a typo.
	mux.HandleFunc("PUT /connections/jira", handlePutJiraConn)
	mux.HandleFunc("DELETE /connections/jira", handleDeleteJiraConn)
	// OAuth (docs/log/80 §80.17): the CP calls /oauth after the code exchange; the Console
	// never touches it. /site is a Console -> CP proxy — one authorization may cover several
	// sites.
	mux.HandleFunc("PUT /connections/jira/oauth", handleJiraOAuthStore)
	mux.HandleFunc("PUT /connections/jira/site", handlePutJiraSite)
	mux.HandleFunc("POST /connections/claude/start", claude.HandleStart)
	mux.HandleFunc("POST /connections/claude/complete", claude.HandleComplete)
	mux.HandleFunc("DELETE /connections/claude", claude.HandleDisconnect)
	mux.HandleFunc("PUT /connections/opencode", opencode.HandlePutConn)
	mux.HandleFunc("DELETE /connections/opencode/{env}", opencode.HandleDeleteConn)
	// opencode Console account (device flow, docs/log/54). Coexists with the API-key method.
	mux.HandleFunc("POST /connections/opencode/oauth/start", opencode.HandleOAuthStart)
	mux.HandleFunc("POST /connections/opencode/oauth/poll", opencode.HandleOAuthPoll)
	mux.HandleFunc("POST /connections/opencode/oauth/cancel", opencode.HandleOAuthCancel)
	mux.HandleFunc("DELETE /connections/opencode/oauth", opencode.HandleOAuthDisconnect)
	// The id behind the link to the quota page (opencode.ai/workspace/{id}/go). Filled either
	// by hand or learned automatically from a limit / balance error (docs/log/54 §54.7).
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
	// cursor login (docs/log/40 Track C): a dedicated flow like claude/agy, but with no pasted
	// code — it polls for browser approval the way codex device-auth does, hence start -> poll.
	mux.HandleFunc("POST /connections/cursor/start", cursor.HandleStart)
	mux.HandleFunc("POST /connections/cursor/poll", cursor.HandlePoll)
	mux.HandleFunc("DELETE /connections/cursor", cursor.HandleDisconnect)
	// kiro login (docs/log/43 Track C): a device flow, start -> poll like codex/cursor.
	// kiro-cli polls AWS SSO itself and writes the credentials once approved. Nothing is
	// pasted; it only shows a URL and a verification code.
	mux.HandleFunc("POST /connections/kiro/start", kiro.HandleStart)
	mux.HandleFunc("POST /connections/kiro/poll", kiro.HandlePoll)
	mux.HandleFunc("DELETE /connections/kiro", kiro.HandleDisconnect)
	// kiro on-demand install (docs/log/43 Track B/C): the lean image does not bake the ~855MB
	// bundle, so the connection card's Install button comes here. POST starts a background
	// install, GET polls its progress.
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
	// Chat bridge (docs/log/37 P1): the Discord bot token plus its destination.
	// inspect/guilds serve the card's setup wizard (build an invite link -> channel picker).
	mux.HandleFunc("PUT /connections/discord", handlePutDiscordConn)
	mux.HandleFunc("DELETE /connections/discord", handleDeleteDiscordConn)
	mux.HandleFunc("POST /connections/discord/inspect", handleDiscordInspect)
	mux.HandleFunc("POST /connections/discord/guilds", handleDiscordGuilds)
	// Chat bridge Slack (docs/log/37, Slack parity): bot xoxb- plus app-level xapp- token,
	// plus the destination. inspect/channels serve the setup wizard (token validation ->
	// channel picker + email resolution).
	mux.HandleFunc("PUT /connections/slack", handlePutSlackConn)
	mux.HandleFunc("DELETE /connections/slack", handleDeleteSlackConn)
	mux.HandleFunc("POST /connections/slack/inspect", handleSlackInspect)
	mux.HandleFunc("POST /connections/slack/channels", handleSlackChannels)

	return mux
}
