// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	// Subcommand mode: git invokes this binary as its credential helper
	// (`workspace-agent cred get`), backed by the encrypted store. It prints
	// creds and exits without starting the server. `bitbucket-cred` is kept as
	// an alias for any git config left over from before the unified helper.
	if len(os.Args) > 1 && (os.Args[1] == "cred" || os.Args[1] == "bitbucket-cred") {
		runCredHelper(os.Args[2:])
		return
	}
	// claude hook helper: records session working/idle/question state.
	if len(os.Args) > 1 && os.Args[1] == "session-status" {
		runSessionStatusHook(os.Args[2:])
		return
	}
	// Local stdio MCP server for the assistant chat (docs/19 Q1): read-only Agent
	// Fleet tools over JSON-RPC on stdio, spawned by claude's --mcp-config.
	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		runMCPStdio(os.Args[2:])
		return
	}

	// Fold any pre-A3 plaintext credential files into the encrypted store.
	migrateLegacySecrets()
	// Make claude emit working/idle/question via hooks into the status files.
	ensureStatusHooks()
	// Apply the durable codex/opencode rtk prefs to their artifacts (the entrypoint
	// reseeded the base AGENTS.md / status plugin just before us). claude's rtk is
	// handled separately via its settings.json hook.
	reconcileAgentRTK()
	// Restart the opencode web UI (opencode serve + pk-webui) if its durable toggle
	// is on. Best-effort; failure leaves the rest of the agent running.
	reconcileOpencodeWeb()

	addr := envOr("AGENT_ADDR", ":7700")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("POST /sessions", handleCreateSession)
	mux.HandleFunc("POST /sessions/{name}/fork", handleForkSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("POST /sessions/{name}/halt", handleHaltSession)
	mux.HandleFunc("POST /sessions/{name}/recreate", handleRecreateSession)
	mux.HandleFunc("GET /sessions/archived", handleListArchived)
	mux.HandleFunc("POST /sessions/{name}/archive", handleArchiveSession)
	mux.HandleFunc("POST /sessions/{name}/restore", handleRestoreSession)
	// Programmatic drive I/O for the MCP tools (docs/0006 P3-6 E).
	mux.HandleFunc("POST /sessions/{name}/input", handleSessionInput)
	mux.HandleFunc("POST /sessions/{name}/paste-image", handlePasteImage)
	mux.HandleFunc("GET /sessions/{name}/pasted/{file}", handlePastedImage)
	mux.HandleFunc("GET /sessions/{name}/status", handleSessionStatus)
	mux.HandleFunc("GET /sessions/{name}/output", handleSessionOutput)
	mux.HandleFunc("GET /sessions/{name}/ssm-login", handleSSMLoginStatus)
	mux.HandleFunc("POST /sessions/{name}/start", handleStartSession)
	// Structured transcript (role + text + timestamp) for the Console chat view.
	mux.HandleFunc("GET /sessions/{name}/messages", handleSessionMessages)
	// Auto session-title suggestion (session_title.go): accept promotes it to Title,
	// dismiss discards it — either way it's never offered again for this session.
	mux.HandleFunc("POST /sessions/{name}/title/accept", handleAcceptSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/dismiss", handleDismissSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/regenerate", handleRegenerateSuggestedTitle)
	mux.HandleFunc("POST /sessions/{name}/title/suggest", handleSuggestTitle)
	mux.HandleFunc("POST /sessions/{name}/title/set", handleSetTitle)
	mux.HandleFunc("GET /ws/pty", handlePTY)

	// Assistant chat — headless-CLI LLM chat/translation, separate from tmux
	// sessions (docs/19). Non-streaming; the CP proxies these verbatim.
	mux.HandleFunc("GET /chat/conversations", handleChatList)
	mux.HandleFunc("POST /chat/conversations", handleChatCreate)
	mux.HandleFunc("GET /chat/conversations/{id}", handleChatGet)
	mux.HandleFunc("PATCH /chat/conversations/{id}", handleChatRename)
	mux.HandleFunc("DELETE /chat/conversations/{id}", handleChatDelete)
	mux.HandleFunc("POST /chat/conversations/{id}/messages", handleChatSend)
	mux.HandleFunc("POST /chat/conversations/{id}/stream", handleChatStream) // SSE (Phase B)
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", handleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", handleChatPastedImage)
	// Assistant-to-assistant consult (docs/19): af_write orchestrators' ask_assistant tool
	// hits this via the local stdio MCP. Internal (Agent REST) only — not proxied by the CP.
	mux.HandleFunc("POST /chat/ask", handleChatAsk)

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
	mux.HandleFunc("GET /claude/settings", handleClaudeSettingsGet)
	mux.HandleFunc("PUT /claude/settings", handleClaudeSettingsPut)
	// Claude subscription usage (5-hour + weekly bars) for the WsBar chip.
	mux.HandleFunc("GET /claude/usage", handleClaudeUsage)
	mux.HandleFunc("GET /codex/usage", handleCodexUsage)
	// codex / opencode rtk toggle (durable pref → on-disk artifacts) — Console.
	mux.HandleFunc("GET /agents/rtk", handleAgentRTKGet)
	mux.HandleFunc("PUT /agents/rtk", handleAgentRTKPut)
	// opencode web (opencode serve + pk-webui) toggle + its /ocweb proxy — Console.
	mux.HandleFunc("GET /agents/opencode-web", handleOpencodeWebGet)
	mux.HandleFunc("PUT /agents/opencode-web", handleOpencodeWebPut)
	mux.HandleFunc("/ocweb/{rest...}", handleOcwebProxy)

	// Toolchain selection (node via nvm / java via pre-baked Temurin) — Console.
	mux.HandleFunc("GET /env/toolchains", handleToolchainsGet)
	mux.HandleFunc("PUT /env/toolchains", handleToolchainsPut)

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
	mux.HandleFunc("POST /connections/claude/start", handleClaudeStart)
	mux.HandleFunc("POST /connections/claude/complete", handleClaudeComplete)
	mux.HandleFunc("DELETE /connections/claude", handleClaudeDisconnect)
	mux.HandleFunc("PUT /connections/opencode", handlePutOpencodeConn)
	mux.HandleFunc("DELETE /connections/opencode/{env}", handleDeleteOpencodeConn)
	mux.HandleFunc("POST /connections/codex/api-key", handleCodexApiKey)
	mux.HandleFunc("POST /connections/codex/device/start", handleCodexDeviceStart)
	mux.HandleFunc("POST /connections/codex/device/poll", handleCodexDevicePoll)
	mux.HandleFunc("DELETE /connections/codex", handleCodexDisconnect)

	log.Printf("workspace-agent listening on %s", addr)
	if err := http.ListenAndServe(addr, logRequests(requireToken(mux))); err != nil {
		log.Fatal(err)
	}
}

// requireToken enforces the CP↔Agent shared token (docs/07 §7.5): the Control
// Plane injects AGENT_TOKEN at container start and presents it as a Bearer on
// every request. /healthz stays open (used for the startup readiness probe).
// If AGENT_TOKEN is unset the gate is disabled — a safety valve for manual
// debugging; the CP always injects one in normal operation.
func requireToken(next http.Handler) http.Handler {
	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		log.Printf("WARNING: AGENT_TOKEN unset — CP↔Agent auth disabled (dev only)")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid agent token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- small helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}
