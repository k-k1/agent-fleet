package main

import (
	"log"
	"net/http"
	"strings"
)

// buildMux は CP の全ルートを登録した mux を返す（docs/23 P0-2: main() からの機械的
// 抽出）。テストが実ルート表を httptest で叩けるようにするための分離で、登録内容は
// main() にあったものと同一。認証ゲート（authGate）や logRequests のラップは呼び出し
// 側（main / テスト）の責務。
func buildMux(cfg config) *http.ServeMux {
	mux := http.NewServeMux()

	// Health + CP-native Google OAuth (AUTH=oauth). The login page, OAuth
	// endpoints and health check are reachable without a session (authGate
	// exempts them); see oauth_google.go.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /login", cfg.handleLogin)
	mux.HandleFunc("GET /oauth2/login", cfg.handleOAuthLogin)
	mux.HandleFunc("GET /oauth2/callback", cfg.handleOAuthCallback)
	mux.HandleFunc("GET /oauth2/logout", cfg.handleOAuthLogout)

	// Identity — who the AuthGateway resolved this request to (and the raw
	// gateway headers, for verifying the oauth2-proxy -> Caddy -> CP chain).
	mux.HandleFunc("GET /api/whoami", cfg.handleWhoami)

	// Tenants — the caller's memberships (Console picker) + minimal admin API
	// (super_admin only; full UI is P3-5). docs/14 P3-2.
	mux.HandleFunc("GET /api/tenants", cfg.handleTenants)
	mux.HandleFunc("GET /api/admin/tenants", cfg.handleAdminListTenants)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members", cfg.handleAdminListMembers)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/stats", cfg.handleAdminMemberStats)       // per-member mem/CPU/disk
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/sessions", cfg.handleAdminMemberSessions) // per-member session list (read-only)
	mux.HandleFunc("POST /api/admin/tenants", cfg.handleAdminCreateTenant)
	mux.HandleFunc("POST /api/admin/memberships", cfg.handleAdminAddMembership)
	mux.HandleFunc("POST /api/admin/stop-workspace", cfg.handleAdminStopWorkspace)
	mux.HandleFunc("POST /api/admin/clean-home", cfg.handleAdminCleanHome) // wipe home (keep auth/connections)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/limits", cfg.handleAdminSetTenantLimits)
	mux.HandleFunc("PUT /api/admin/user-limits", cfg.handleAdminSetUserLimit)
	mux.HandleFunc("PUT /api/admin/membership-role", cfg.handleAdminSetMembershipRole)           // grant/revoke tenant_admin (super_admin only)
	mux.HandleFunc("GET /api/admin/host", cfg.handleHostStats)                                   // host load / memory (super_admin)
	mux.HandleFunc("GET /api/admin/usage", cfg.handleAdminUsage)                                 // showback: occupancy per tenant/member (json|csv)
	mux.HandleFunc("GET /api/admin/sessions", cfg.handleAdminAllSessions)                        // deployment-wide session overview (super_admin / tenant_admin)
	mux.HandleFunc("GET /api/admin/audit", cfg.handleAdminAudit)                                 // audit log ledger (super_admin / tenant_admin, docs/20 M1)
	mux.HandleFunc("GET /api/admin/egress", cfg.handleAdminEgress)                               // egress observation stats (super_admin, docs/20 M2)
	mux.HandleFunc("POST /internal/egress", cfg.handleEgressIngest)                              // egress proxy -> CP ingestion (AF_EGRESS_TOKEN, docs/20 M2)
	mux.HandleFunc("GET /internal/egress/policy", cfg.handleEgressPolicy)                        // effective allowlist+mode -> proxy (docs/20 M3)
	mux.HandleFunc("GET /api/admin/egress/allowlist", cfg.handleAdminAllowlistList)              // allowlist entries (super_admin, docs/20 M3)
	mux.HandleFunc("POST /api/admin/egress/allowlist", cfg.handleAdminAllowlistAdd)              // add allowlist entry (super_admin)
	mux.HandleFunc("POST /api/admin/egress/allowlist/{id}/state", cfg.handleAdminAllowlistState) // approve/retire (super_admin)
	mux.HandleFunc("GET /api/admin/egress/mode", cfg.handleAdminEgressMode)                      // read egress mode (super_admin)
	mux.HandleFunc("PUT /api/admin/egress/mode", cfg.handleAdminEgressMode)                      // set log-only/enforce (super_admin)

	// Personal Access Tokens (Console-issued) for the MCP endpoint (docs/0006).
	mux.HandleFunc("GET /api/pat", cfg.handlePATList)
	mux.HandleFunc("POST /api/pat", cfg.handlePATCreate)
	mux.HandleFunc("DELETE /api/pat/{id}", cfg.handlePATRevoke)

	// MCP endpoint (P3-6) — opt-in. Bearer PAT auth (not the gateway header), so
	// the ingress must pass /mcp through without oauth2-proxy.
	if envOr("AF_MCP_ENABLED", "") == "true" {
		mux.HandleFunc("/mcp", cfg.handleMCP)
		log.Printf("MCP endpoint enabled at /mcp")
	}

	// Workspace lifecycle (local Docker Runtime adapter).
	mux.HandleFunc("GET /api/workspace", cfg.handleWorkspaceGet)
	mux.HandleFunc("POST /api/workspace/start", cfg.handleWorkspaceStart)
	mux.HandleFunc("POST /api/workspace/stop", cfg.handleWorkspaceStop)
	mux.HandleFunc("POST /api/workspace/recreate", cfg.handleWorkspaceRecreate)
	// Own-workspace resource chip (mem / CPU vs quota) — host-read cgroup, all users.
	mux.HandleFunc("GET /api/workspace/stats", cfg.handleWorkspaceStats)

	// Session ops — proxied to the Workspace Agent.
	mux.HandleFunc("GET /api/sessions", cfg.handleSessionsList)
	mux.HandleFunc("POST /api/sessions", cfg.handleSessionCreate)
	mux.HandleFunc("POST /api/sessions/{name}/fork", cfg.handleSessionFork)
	mux.HandleFunc("POST /api/sessions/{name}/stop", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/halt", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/recreate", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/archived", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/archive", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/restore", cfg.proxyAgentREST)
	// Programmatic drive I/O (docs/0006 P3-6 E) — proxied to the Agent. Also used
	// by the MCP tools, which call the Agent directly via the resolved runtime.
	mux.HandleFunc("POST /api/sessions/{name}/input", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/paste-image", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/pasted/{file}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/output", cfg.proxyAgentREST)
	// SSM login status polled by the New Session modal (docs/history/p3-ssm-session.md)
	// — surfaces the device-auth URL and the "ready" transition without attaching yet.
	mux.HandleFunc("GET /api/sessions/{name}/ssm-login", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/start", cfg.handleSessionStart)
	// Structured transcript for the Console chat view (case-A).
	mux.HandleFunc("GET /api/sessions/{name}/messages", cfg.proxyAgentREST)
	// Auto session-title suggestion accept/dismiss (session_title.go, Agent-side).
	mux.HandleFunc("POST /api/sessions/{name}/title/accept", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/dismiss", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/regenerate", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/suggest", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/set", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/suggest-branch", cfg.proxyAgentREST) // LLM branch-name suggestion (this session's convo)
	mux.HandleFunc("POST /api/sessions/{name}/rename-branch", cfg.proxyAgentREST)  // worktree deferred-naming: git branch -m

	// Assistant chat (docs/19) — headless-CLI LLM chat/translation, proxied to the
	// Agent verbatim (kind-agnostic; non-streaming, so the plain REST proxy suffices).
	mux.HandleFunc("GET /api/chat/conversations", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("PATCH /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations/{id}/messages", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations/{id}/stream", cfg.proxyAgentStream) // SSE (Phase B)
	mux.HandleFunc("POST /api/chat/conversations/{id}/paste-image", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/chat/conversations/{id}/pasted/{file}", cfg.proxyAgentREST)
	// One-shot advisory turn (docs/21 メモ整理) — stateless, tools off. Proxied verbatim.
	mux.HandleFunc("POST /api/chat/ask", cfg.proxyAgentREST)

	// Assistant templates (docs/19 Q2) — configurable chat personas, proxied verbatim.
	mux.HandleFunc("GET /api/assistants", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/assistants", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/assistants/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/assistants/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/assistants/{id}", cfg.proxyAgentREST)

	// SSM login config (docs/history/p3-ssm-session.md) — per-member profiles (common
	// auth bundle) + host bookmarks (per-instance). No AWS secrets; the aws CLI in the
	// workspace authenticates via SSO.
	mux.HandleFunc("GET /api/ssm/profiles", cfg.handleSSMProfilesList)
	mux.HandleFunc("POST /api/ssm/profiles", cfg.handleSSMProfileCreate)
	mux.HandleFunc("PUT /api/ssm/profiles/{id}", cfg.handleSSMProfileUpdate)
	mux.HandleFunc("DELETE /api/ssm/profiles/{id}", cfg.handleSSMProfileDelete)
	mux.HandleFunc("GET /api/ssm/hosts", cfg.handleSSMHostsList)
	mux.HandleFunc("POST /api/ssm/hosts", cfg.handleSSMHostCreate)
	mux.HandleFunc("PUT /api/ssm/hosts/{id}", cfg.handleSSMHostUpdate)
	mux.HandleFunc("DELETE /api/ssm/hosts/{id}", cfg.handleSSMHostDelete)

	// Memo queue (docs/21) — per-member notes accumulated across devices, then flushed
	// to a session as one message. Scoped by membership (no workspace build for CRUD).
	mux.HandleFunc("GET /api/memos", cfg.handleMemosList)
	mux.HandleFunc("POST /api/memos", cfg.handleMemoCreate)
	mux.HandleFunc("POST /api/memos/flush", cfg.handleMemoFlush)
	mux.HandleFunc("PATCH /api/memos/{id}", cfg.handleMemoUpdate)
	mux.HandleFunc("DELETE /api/memos/{id}", cfg.handleMemoDelete)

	// Repository ops — proxied to the Workspace Agent (/api stripped -> /repos*).
	mux.HandleFunc("GET /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/repos/{name}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/checkout", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/fetch", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/ff", cfg.proxyAgentREST)
	// Launch prompt templates (repo 起動 modal) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/prompt-templates", cfg.proxyAgentREST)
	// Source-control view + light edits (docs/17 P3-5) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/changes", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/diff", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/log", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/graph", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/show", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/stage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/unstage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/discard", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/commit", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/identity", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/repos/{name}/identity", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/git/identity", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/git/identity", cfg.proxyAgentREST)
	// File browser (docs/17 P3-5 段2) — proxied to the Agent.
	mux.HandleFunc("GET /api/fs/tree", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/file", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/download", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/upload", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/changes", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/linemarks", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/mkdir", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/newfile", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/rename", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/fs/delete", cfg.proxyAgentREST)

	// Claude settings (Remote Control / notifications / RTK) — proxied to the Agent.
	mux.HandleFunc("GET /api/claude/settings", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/claude/settings", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/claude/usage", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/codex/usage", cfg.proxyAgentREST)
	// codex / opencode rtk toggle — proxied to the Agent.
	mux.HandleFunc("GET /api/agents/rtk", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/agents/rtk", cfg.proxyAgentREST)

	// Toolchain selection (node / java) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/toolchains", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/toolchains", cfg.proxyAgentREST)
	// CP-owned per-workspace settings (editable while stopped; applied at start).
	mux.HandleFunc("GET /api/env/ws-settings", cfg.handleWSSettingsGet)
	mux.HandleFunc("PUT /api/env/ws-settings", cfg.handleWSSettingsPut)

	// Per-user UI preferences (Console display settings) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/ui-prefs", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/ui-prefs", cfg.proxyAgentREST)

	// Connections ops — proxied to the Workspace Agent (/api stripped).
	mux.HandleFunc("GET /api/connections", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/repos", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/git/{host}", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/git/{host}/identity", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/git/{host}", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/git/github/oauth/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/git/github/oauth/poll", cfg.proxyAgentREST)
	// Bitbucket OAuth — CP-native (owns the public callback), not proxied.
	mux.HandleFunc("GET /api/connections/git/bitbucket/oauth/start", cfg.handleBitbucketOAuthStart)
	mux.HandleFunc("GET /api/oauth/bitbucket/callback", cfg.handleBitbucketOAuthCallback)
	mux.HandleFunc("POST /api/connections/claude/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/claude/complete", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/claude", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/opencode", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/opencode/{env}", cfg.proxyAgentREST)
	// Codex auth — proxied to the Agent (codex owns auth.json; no public callback,
	// device-auth polls OpenAI from inside the container).
	mux.HandleFunc("POST /api/connections/codex/api-key", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/codex/device/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/codex/device/poll", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/codex", cfg.proxyAgentREST)

	// Internal git provider (docs/reference/internal-git-provider, ADR 0010).
	// Repo management is CP-native (the CP owns the bare repos), so these are NOT
	// proxied to the Agent like other providers.
	mux.HandleFunc("GET /api/internal-git/repos", cfg.handleInternalGitReposList)
	mux.HandleFunc("POST /api/internal-git/repos", cfg.handleInternalGitRepoCreate)
	mux.HandleFunc("DELETE /api/internal-git/repos/{name}", cfg.handleInternalGitRepoDelete)
	mux.HandleFunc("POST /api/internal-git/repos/{name}/rename", cfg.handleInternalGitRepoRename)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/branches", cfg.handleInternalGitBranches)
	// Read-only browsing (clone-free): tree / blob / commits, served from the bare.
	mux.HandleFunc("GET /api/internal-git/repos/{name}/tree", cfg.handleInternalGitTree)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/blob", cfg.handleInternalGitBlob)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/commits", cfg.handleInternalGitCommits)
	// Git LFS face (docs/reference/internal-git-provider, P3). More specific than the
	// smart-HTTP catch-all below, so these win for LFS paths; git-http-backend never
	// sees them. Same Basic git-token auth (session-exempt under /git/).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/objects/batch", cfg.handleLFSBatch)
	mux.HandleFunc("PUT /git/{slug}/{repo}/info/lfs/objects/{oid}", cfg.handleLFSUpload)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/objects/{oid}", cfg.handleLFSDownload)
	// LFS file locking API (create / list / verify / unlock).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks", cfg.handleLFSLockCreate)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/locks", cfg.handleLFSLocksList)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/verify", cfg.handleLFSLocksVerify)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/{id}/unlock", cfg.handleLFSUnlock)

	// Smart-HTTP git face (clone/fetch/push). Self-authenticating via a Basic git
	// token (session-exempt, like /mcp); handles every method.
	mux.HandleFunc("/git/{slug}/{repo...}", cfg.handleGitHTTP)

	// Terminal PTY — proxied WebSocket.
	mux.HandleFunc("GET /ws/terminal", cfg.proxyTerminal)

	// Preview — proxy to a service the user started inside their container
	// (Spring Boot, dev server, ...) via the Agent's /proxy/{port}. The redirect
	// adds the trailing slash so the app resolves relative assets under the path.
	mux.HandleFunc("/preview/{port}", cfg.handlePreviewRedirect)
	mux.HandleFunc("/preview/{port}/{rest...}", cfg.handlePreview)

	// Legacy path compatibility: the deployment used to be served under
	// /agent-fleet (oauth2-proxy + Caddy stripped it). Now it's at the root, so
	// old bookmarks — and any stale post-login next=/agent-fleet/… — would 404.
	// Redirect /agent-fleet[/…] -> /… (auth-exempt, so it fires before login and
	// the dead prefix never reaches next=).
	legacyRedirect := func(w http.ResponseWriter, r *http.Request) {
		dest := strings.TrimPrefix(r.URL.Path, "/agent-fleet")
		if !strings.HasPrefix(dest, "/") {
			dest = "/" + dest
		}
		if r.URL.RawQuery != "" {
			dest += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, dest, http.StatusFound)
	}
	mux.HandleFunc("/agent-fleet", legacyRedirect)
	mux.HandleFunc("/agent-fleet/", legacyRedirect)

	// Static Console (catch-all). no-store so reloads always get fresh assets
	// during active development.
	mux.Handle("/", noStore(http.FileServer(http.Dir(cfg.consoleDir))))

	return mux
}
