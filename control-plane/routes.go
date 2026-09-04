package main

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// buildMux returns a mux with every CP route registered, spread over per-feature
// register functions (docs/log/23 P2-W1). Keeping it out of main() is what lets a test
// drive the real route table through httptest. Wrapping it in the auth gate (authGate)
// or in logRequests is the caller's job (main / the test).
func buildMux(cfg config) *http.ServeMux {
	mux := http.NewServeMux()
	registerAuthRoutes(mux, cfg)
	registerTenantAdminRoutes(mux, cfg)
	registerPATRoutes(mux, cfg)
	registerMCPRoutes(mux, cfg)
	registerWorkspaceRoutes(mux, cfg)
	registerEventsRoutes(mux, cfg)
	registerSessionRoutes(mux, cfg)
	registerSessionShareRoutes(mux, cfg)
	registerChatRoutes(mux, cfg)
	registerAssistantRoutes(mux, cfg)
	registerTTSRoutes(mux, cfg)
	registerSSMRoutes(mux, cfg)
	registerMemoRoutes(mux, cfg)
	registerWorkItemRoutes(mux, cfg)
	registerScheduleRoutes(mux, cfg)
	registerMCPServerRoutes(mux, cfg)
	registerNotificationRoutes(mux, cfg)
	registerUpdateRoutes(mux, cfg)
	registerRepoFSRoutes(mux, cfg)
	registerAgentEnvRoutes(mux, cfg)
	registerConnectionRoutes(mux, cfg)
	registerInternalGitRoutes(mux, cfg)
	registerBrowserRoutes(mux, cfg)
	registerDrawioStencilRoutes(mux, cfg)
	registerTerminalPreviewRoutes(mux, cfg)
	registerLegacyRedirect(mux)
	registerStatic(mux, cfg)
	return mux
}

func registerNotificationRoutes(mux *http.ServeMux, cfg config) {
	n := newNotificationAPI(cfg.mgr)
	mux.HandleFunc("GET /api/notifications", n.withResolved(n.list))
	mux.HandleFunc("POST /api/notifications/seen", n.withMembership(n.seen))
	mux.HandleFunc("POST /api/notifications/usage-observations", n.withMembership(n.observeUsage))
}

// --- authGate exemption registry (docs/log/23 P2-W1) ---------------------------
// Paths reachable without a session were hardcoded in oauth_google.go and kept in sync
// with the route table by hand. Each register function now declares its own exemptions
// and authGate's isAuthExempt consults this registry. buildMux is called more than once
// from tests, so registration is idempotent (re-adding to a set).

var (
	authExemptMu       sync.Mutex
	authExemptExact    = map[string]bool{}
	authExemptPrefixes = map[string]bool{}
)

func exemptExact(paths ...string) {
	authExemptMu.Lock()
	defer authExemptMu.Unlock()
	for _, p := range paths {
		authExemptExact[p] = true
	}
}

func exemptPrefix(prefixes ...string) {
	authExemptMu.Lock()
	defer authExemptMu.Unlock()
	for _, p := range prefixes {
		authExemptPrefixes[p] = true
	}
}

// isAuthExempt reports whether p is reachable without a session (consumed by
// authGate, oauth_google.go). The set is declared next to each route group below.
func isAuthExempt(p string) bool {
	authExemptMu.Lock()
	defer authExemptMu.Unlock()
	if authExemptExact[p] {
		return true
	}
	for pre := range authExemptPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// --- Per-feature route registration --------------------------------------------

// Health + CP-native OIDC login (AUTH=oauth) + identity. The login page, OAuth
// endpoints, health check and the login page's brand asset are reachable without
// a session; see oauth.go (flow/session) and oauth_oidc.go (the IdP client).
func registerAuthRoutes(mux *http.ServeMux, cfg config) {
	exemptExact("/login", "/healthz", "/readyz")
	// /login/<tenant-slug> is the per-tenant sign-in page (docs/log/61 §61.9.3) and is
	// reachable without a session, like /login itself.
	exemptPrefix("/oauth2/", "/brand/", "/login/")
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	// /readyz — liveness is not readiness. /healthz above says "ok" as long as the
	// process is up; on 2026-09-01 it did exactly that for fifteen minutes while RDS
	// rotated the password out from under the running task and every /api/* returned
	// 500. ALB targets healthy, ECS steady, CloudFormation unchanged, product dead.
	//
	// ⚠️ The ALB health check stays on /healthz on purpose (30-ingress.yaml
	// HealthCheckPath). Pointing it here would let a momentary RDS blip kill the CP
	// task, and the CP runs at desiredCount 1 — a restart loop bought in exchange for
	// a self-heal that store_postgres.go now performs without one.
	//
	// The body says nothing an unauthenticated caller should not see: no DSN, no
	// user, no SQLSTATE. Whoever can act on the detail can read the log.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := cfg.mgr.store.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// Build version + the images this deployment runs (docs/log/35 §35.6.1,
	// version_info.go). Deliberately NOT auth-exempt: /healthz is frozen
	// (restart-cp.sh compares the body to "ok" verbatim) and none of this should
	// leak to unauthenticated callers.
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, versionPayload(r.Context(), cfg.mgr))
	})
	mux.HandleFunc("GET /login", cfg.handleLogin)
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin) // per-tenant page (docs/log/61 §61.9.3)
	mux.HandleFunc("GET /oauth2/login", cfg.handleOAuthLogin)
	mux.HandleFunc("GET /oauth2/callback", cfg.handleOAuthCallback)
	mux.HandleFunc("GET /oauth2/logout", cfg.handleOAuthLogout)
	// Linking a second sign-in method to the account you are ALREADY signed in as
	// (docs/log/61 §61.16 + decision 37). It sits under the auth-exempt /oauth2/ prefix like
	// the rest of the flow, and therefore checks the session itself.
	mux.HandleFunc("GET /oauth2/link", cfg.handleOAuthLink)
	// The account panel behind it — a normal session-gated API (docs/log/61 §61.16).
	acct := newAccountAPI(cfg)
	mux.HandleFunc("GET /api/me/login-methods", acct.withIdentity(acct.loginMethods))
	// Unlinking one (docs/log/61 §61.16.4). provider/subject are query parameters, not
	// path segments — a tenant provider id carries colons; see detachLoginMethod.
	mux.HandleFunc("DELETE /api/me/login-methods", acct.withIdentity(acct.detachLoginMethod))
	// Identity — who the AuthGateway resolved this request to (and the raw
	// gateway headers, for verifying the oauth2-proxy -> Caddy -> CP chain).
	mux.HandleFunc("GET /api/whoami", newWorkspaceAPI(cfg.mgr, cfg.autostart).whoami)
}

// Tenants — the caller's memberships (Console picker) + admin API + egress
// observation (docs/14 P3-2, docs/log/20). /internal/* is deployment-internal
// (egress proxy ingestion), authenticated by Bearer token — session-exempt.
func registerTenantAdminRoutes(mux *http.ServeMux, cfg config) {
	exemptPrefix("/internal/")
	tn := newTenantAPI(cfg.mgr)
	adm := newAdminAPI(cfg.mgr)
	eg := newEgressAPI(cfg.mgr, cfg.egressToken, cfg.egressProxyAddr, cfg.egressDedup)
	mux.HandleFunc("GET /api/tenants", tn.withIdentity(tn.list))
	mux.HandleFunc("GET /api/admin/tenants", adm.withIdentity(adm.listTenants))
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members", adm.listMembers)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/stats", adm.memberStats)       // per-member mem/CPU/disk
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/sessions", adm.memberSessions) // per-member session list (read-only)
	// One member's attributed cloud cost (docs/log/67 §67.15). Same body as /api/cost/me,
	// keyed by membership instead of by the caller — the per-member LIST carries a total
	// and nothing else, so the daily shape and the breakdown are not derivable from it.
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/cost", adm.memberCloudCost)
	// One member's hourly occupancy — the heatmap next to the stop / disk-quota buttons
	// (docs/log/83). Same body as /api/usage/me/hourly, keyed by membership.
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/usage-hourly", adm.memberUsageHourly)
	// The third and last cleanup step (docs/log/61 §61.18): only a row already offboarded
	// (DELETE /api/admin/memberships) and destroyed (DELETE /api/admin/workspaces) is
	// actually removed here.
	mux.HandleFunc("DELETE /api/admin/tenants/{slug}/members/{key}", adm.deleteMembership)
	mux.HandleFunc("POST /api/admin/tenants", adm.withSuperAdmin(adm.createTenant))
	mux.HandleFunc("DELETE /api/admin/tenants/{slug}", adm.withSuperAdmin(adm.deleteTenant)) // empty tenants only (docs/log/61 §61.18)
	mux.HandleFunc("POST /api/admin/memberships", adm.addMembership)
	mux.HandleFunc("DELETE /api/admin/memberships", adm.removeMembership) // offboarding (docs/log/61 §61.10.6)
	mux.HandleFunc("DELETE /api/admin/workspaces", adm.destroyWorkspace)  // irreversible; inactive members only (ADR 0045 decision 13)
	mux.HandleFunc("POST /api/admin/stop-workspace", adm.stopWorkspace)
	mux.HandleFunc("POST /api/admin/clean-home", adm.cleanHome) // wipe home (tenant_admin, docs/log/61 §61.10.6)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/limits", adm.withSuperAdmin(adm.setTenantLimits))
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/login", adm.withSuperAdmin(adm.setTenantLogin)) // per-tenant login rules (docs/log/61 §61.9.7)
	// The tenant's own source-network restriction (docs/log/66, ADR 0047). tenant_admin,
	// unlike the login rules above: it reaches nothing outside this tenant. The gate
	// on both is checked mid-handler by tenantAdminFor.
	mux.HandleFunc("GET /api/admin/tenants/{slug}/network", adm.tenantNetwork)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/network", adm.setTenantNetwork)
	// The tenant's DEFAULT machine class (docs/log/70). tenant_admin for the same reason
	// as the network rule above — it reaches nothing outside this tenant, and the set
	// it may choose from is bounded by allowed_slot_classes, which only a super_admin
	// can write (on the limits endpoint).
	mux.HandleFunc("GET /api/admin/tenants/{slug}/slot-class", adm.tenantSlotClass)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/slot-class", adm.setTenantSlotClass)
	// Tenant-defined sign-in methods (docs/log/61 §61.11). The rows are the tenant's, so
	// these gate on tenant_admin mid-handler; ACTIVATION is checked inside setStatus,
	// which is the one super_admin step (decision 30). The queue is deployment-wide.
	idp := newTenantIdPAPI(cfg.mgr, cfg.providers)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/idp", idp.list)
	mux.HandleFunc("POST /api/admin/tenants/{slug}/idp", idp.upsert)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/idp/{id}", idp.upsert)
	mux.HandleFunc("DELETE /api/admin/tenants/{slug}/idp/{id}", idp.remove)
	mux.HandleFunc("POST /api/admin/tenants/{slug}/idp/{id}/status", idp.setStatus)
	mux.HandleFunc("GET /api/admin/idp", idp.withSuperAdmin(idp.queue)) // approval queue (super_admin)
	// The tenant's git provider OAuth apps (docs/log/71). tenant_admin end to end — no
	// approval step, because an app for cloning repositories declares nothing about who
	// anybody is (ADR0052 decision 3). The gate is taken mid-handler by tenantAdminFor.
	gho := newTenantGitOAuthAPI(cfg.mgr)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/git-oauth", gho.list)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/git-oauth/{provider}", gho.save)
	mux.HandleFunc("DELETE /api/admin/tenants/{slug}/git-oauth/{provider}", gho.remove)
	// The deployment's own (env-defined) providers, read-only — since P7 these ARE
	// the default tenant's methods, and every tenant's sign-in method panel lists
	// them (docs/log/61 §61.17). Hence withAnyTenantAdmin rather than withSuperAdmin:
	// the reader is a tenant administrator, and the issuer column is dropped for
	// them inside the handler. cfg.providers is set before buildMux, so capturing
	// it here is the whole set.
	lp := newLoginProviderAPI(cfg.mgr, cfg.providers)
	mux.HandleFunc("GET /api/admin/providers", lp.withAnyTenantAdmin(lp.list))
	mux.HandleFunc("PUT /api/admin/user-limits", adm.setUserLimit)
	mux.HandleFunc("PUT /api/admin/membership-role", adm.withSuperAdmin(adm.setMembershipRole)) // grant/revoke tenant_admin (super_admin only)
	mux.HandleFunc("GET /api/admin/host", adm.withSuperAdmin(adm.hostStats))
	mux.HandleFunc("GET /api/admin/ec2-pool", adm.withSuperAdmin(adm.poolStatus))                   // EC2 slot pool (ecs-ec2 only)                            // host load / memory (super_admin)
	mux.HandleFunc("GET /api/admin/workspace-sizing", adm.withIdentity(adm.workspaceSizingProfile)) // what mem/CPU/disk MEAN on this runtime (ADR 0045 decision 21)
	mux.HandleFunc("GET /api/admin/usage", adm.usage)                                               // showback: occupancy per tenant/member (json|csv)
	// The same occupancy at hour resolution, for the uptime heatmap (docs/log/83). A
	// separate endpoint rather than a `bucket=hour` parameter on the one above: that one
	// answers "how many hours in the window" and streams CSV for chargeback, this one
	// answers "when, and with how many sessions open", and only one of them has a
	// heartbeat series telling the reader which hours were observed at all.
	mux.HandleFunc("GET /api/admin/usage/hourly", adm.adminUsageHourly)
	mux.HandleFunc("GET /api/usage/me/hourly", adm.withMembership(adm.myUsageHourly)) // the caller's OWN occupancy
	// Cloud cost — the AWS invoice (docs/log/67 + ADR 0048). Deliberately NOT folded into
	// /api/admin/usage: that one is occupancy in seconds and exists everywhere, this one
	// is money and exists only on AWS. Same reason the Console gives them different names.
	mux.HandleFunc("GET /api/cost/profile", adm.withIdentity(adm.costProfileHandler))                   // is there a bill here at all
	mux.HandleFunc("GET /api/cost/me", adm.withMembership(adm.myCloudCost))                             // the caller's OWN attributed spend
	mux.HandleFunc("GET /api/admin/cloud-cost", adm.cloudCost)                                          // per-member totals (+ shared, super_admin only)
	mux.HandleFunc("GET /api/admin/sessions", adm.allSessions)                                          // deployment-wide session overview (super_admin / tenant_admin)
	mux.HandleFunc("GET /api/admin/audit", adm.audit)                                                   // audit log ledger (super_admin / tenant_admin, docs/log/20 M1)
	mux.HandleFunc("GET /api/admin/egress", eg.withSuperAdmin(eg.stats))                                // egress observation stats (super_admin, docs/log/20 M2)
	mux.HandleFunc("POST /internal/egress", eg.ingest)                                                  // egress proxy -> CP ingestion (AF_EGRESS_TOKEN, docs/log/20 M2)
	mux.HandleFunc("GET /internal/egress/policy", eg.policy)                                            // effective allowlist+mode -> proxy (docs/log/20 M3)
	mux.HandleFunc("GET /api/admin/egress/allowlist", eg.withSuperAdmin(eg.allowlistList))              // allowlist entries (super_admin, docs/log/20 M3)
	mux.HandleFunc("POST /api/admin/egress/allowlist", eg.withSuperAdmin(eg.allowlistAdd))              // add allowlist entry (super_admin)
	mux.HandleFunc("POST /api/admin/egress/allowlist/{id}/state", eg.withSuperAdmin(eg.allowlistState)) // approve/retire (super_admin)
	mux.HandleFunc("GET /api/admin/egress/mode", eg.withSuperAdmin(eg.mode))                            // read egress mode (super_admin)
	mux.HandleFunc("PUT /api/admin/egress/mode", eg.withSuperAdmin(eg.mode))                            // set log-only/enforce (super_admin)
	// Member face (docs/log/48 §9): "can a workspace reach this MCP server's host, and if not,
	// let me ask". The write produces a PROPOSED entry only — approval stays super_admin
	// on the routes above, so this cannot widen egress (egress_member.go).
	mux.HandleFunc("GET /api/egress/check", eg.withMembership(eg.checkHosts)) // per-host verdict + deployment mode
	mux.HandleFunc("POST /api/egress/propose", eg.withMembership(eg.propose)) // request an allowlist entry (proposed)
}

// Personal Access Tokens (Console-issued) for the MCP endpoint (docs/0006).
func registerPATRoutes(mux *http.ServeMux, cfg config) {
	pat := newPATAPI(cfg.mgr)
	mux.HandleFunc("GET /api/pat", pat.withIdentity(pat.list))
	mux.HandleFunc("POST /api/pat", pat.withIdentity(pat.create))
	mux.HandleFunc("DELETE /api/pat/{id}", pat.withIdentity(pat.revoke))
}

// MCP endpoint (P3-6) — opt-in. Bearer PAT auth (not the gateway header), so
// the ingress must pass /mcp through without oauth2-proxy. The session
// exemption is declared unconditionally (as before): with MCP disabled the
// path just falls through to the static catch-all.
func registerMCPRoutes(mux *http.ServeMux, cfg config) {
	exemptExact("/mcp")
	exemptPrefix("/mcp/")
	if envx.Or("AF_MCP_ENABLED", "") == "true" {
		mux.HandleFunc("/mcp", mcpsrv.New(cpDeps{cfg.mgr}).HandleMCP)
		log.Printf("MCP endpoint enabled at /mcp")
	}
}

// Workspace lifecycle (Runtime port).
func registerWorkspaceRoutes(mux *http.ServeMux, cfg config) {
	ws := newWorkspaceAPI(cfg.mgr, cfg.autostart)
	mux.HandleFunc("GET /api/workspace", ws.withResolved(ws.get))
	mux.HandleFunc("POST /api/workspace/start", ws.withResolved(ws.start))
	mux.HandleFunc("POST /api/workspace/stop", ws.withResolved(ws.stop))
	mux.HandleFunc("POST /api/workspace/recreate", ws.withResolved(ws.recreate))
	mux.HandleFunc("POST /api/workspace/clean-home", ws.withResolved(ws.cleanHome)) // deeper reset: wipe home except logins/connections
	// Own-workspace resource chip (mem / CPU vs quota) — host-read cgroup, all users.
	mux.HandleFunc("GET /api/workspace/stats", ws.withResolved(ws.stats))
	// Attention beacon (docs/log/75 P3): tells the idle clock that a human is touching the
	// Console right now. Presence judged on keystrokes alone reads someone who is only
	// reading as absent.
	mux.HandleFunc("POST /api/workspace/attention", ws.withResolved(ws.attention))
}

// Session ops — proxied to the Workspace Agent.
func registerSessionRoutes(mux *http.ServeMux, cfg config) {
	ws := newWorkspaceAPI(cfg.mgr, cfg.autostart)
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	mux.HandleFunc("GET /api/sessions", ws.withResolved(ws.sessionsList))
	mux.HandleFunc("POST /api/sessions", ws.withResolved(ws.sessionCreate))
	mux.HandleFunc("POST /api/sessions/{name}/fork", ws.withResolved(ws.sessionFork))
	mux.HandleFunc("POST /api/sessions/{name}/stop", rest)
	mux.HandleFunc("POST /api/sessions/{name}/halt", rest)
	mux.HandleFunc("POST /api/sessions/{name}/recreate", rest)
	mux.HandleFunc("GET /api/sessions/archived", rest)
	mux.HandleFunc("POST /api/sessions/{name}/archive", rest)
	mux.HandleFunc("POST /api/sessions/{name}/restore", rest)
	// Cleanup (docs/log/32): survey, per-session usage, delete-with-reclaim, and the gz
	// safety-net archive (list/restore/purge). Proxied verbatim; also used by the MCP
	// cleanup tools, which reach the Agent directly via the resolved runtime.
	mux.HandleFunc("GET /api/sessions/usage", rest)
	// Per-feature usage time series (docs/log/46 P3 / ADR0029) — relayed verbatim; the
	// Agent has already aggregated it.
	mux.HandleFunc("GET /api/usage/series", rest)
	mux.HandleFunc("GET /api/sessions/cleanup", rest)
	mux.HandleFunc("DELETE /api/sessions/{name}", rest)
	mux.HandleFunc("POST /api/sessions/{name}/lock", rest) // deletion lock (docs/log/45)
	mux.HandleFunc("GET /api/cleanup/archives", rest)
	mux.HandleFunc("POST /api/cleanup/archives/{id}/restore", rest)
	mux.HandleFunc("DELETE /api/cleanup/archives/{id}", rest)
	// Programmatic drive I/O (docs/0006 P3-6 E) — proxied to the Agent. Also used
	// by the MCP tools, which call the Agent directly via the resolved runtime.
	mux.HandleFunc("POST /api/sessions/{name}/input", rest)
	// Semantic turn ops + Interaction reply (docs/log/27 P1.5) — proxied verbatim.
	mux.HandleFunc("POST /api/sessions/{name}/turn", rest)
	mux.HandleFunc("POST /api/sessions/{name}/respond", rest)
	// Plan approval response (docs/log/30 / ExitPlanMode) — lets the Console's plan card
	// send a rejection plus feedback in one action. The Agent takes care of Escape,
	// waiting for the composer to come back, and the submission. Posting to /input while
	// the approval dialog is open feeds the text to the modal and turns Enter into an
	// approval, so this is the only route that delivers it safely.
	mux.HandleFunc("POST /api/sessions/{name}/plan-respond", rest)
	// Answer to a carried-over interaction (docs/log/75) — replies to a question, plan or
	// permission that was still unanswered at stop time; the Agent resumes the session
	// first and delivers the answer as text. The target may be a stopped Workspace, so
	// unlike the other writes this one goes through auto-start (restStart below).
	mux.HandleFunc("POST /api/sessions/{name}/carried-answer", ws.withResolved(ws.sessionCarriedAnswer))
	// Keep-awake pin (docs/log/75) — shields the session and the Workspace from idle
	// auto-stop for a bounded time.
	mux.HandleFunc("POST /api/sessions/{name}/keep-awake", rest)
	// Read and live-update a managed session's ThreadSettings (docs/log/27 P2 §9.4-3) —
	// proxied verbatim.
	mux.HandleFunc("GET /api/sessions/{name}/settings", rest)
	mux.HandleFunc("POST /api/sessions/{name}/settings", rest)
	// Exclusive driver switch (docs/log/27 P3 §2: tui ⇄ managed) — proxied verbatim.
	mux.HandleFunc("POST /api/sessions/{name}/driver", rest)
	mux.HandleFunc("POST /api/sessions/{name}/paste-image", rest)
	mux.HandleFunc("GET /api/sessions/{name}/pasted/{file}", rest)
	mux.HandleFunc("GET /api/sessions/{name}/status", rest)
	mux.HandleFunc("GET /api/sessions/{name}/output", rest)
	// SSM login status polled by the New Session modal (docs/log/p3-ssm-session.md)
	// — surfaces the device-auth URL and the "ready" transition without attaching yet.
	mux.HandleFunc("GET /api/sessions/{name}/ssm-login", rest)
	mux.HandleFunc("POST /api/sessions/{name}/start", ws.withResolved(ws.sessionStart))
	mux.HandleFunc("POST /api/ssm/instances", ws.withResolved(ws.ssmInstances))
	// Structured transcript for the Console chat view (case-A).
	mux.HandleFunc("GET /api/sessions/{name}/messages", rest)
	mux.HandleFunc("GET /api/sessions/{name}/handoff-proposal", rest)
	mux.HandleFunc("POST /api/sessions/{name}/handoff-proposal", rest)
	mux.HandleFunc("DELETE /api/sessions/{name}/handoff-proposal", rest)
	// Transcript marks (docs/log/69 / ADR 0050) — the owner's Console route. The
	// recipient's route is in registerSessionShareRoutes, where the CP evaluates the ACL
	// and stamps the author.
	mux.HandleFunc("GET /api/sessions/{name}/marks", rest)
	mux.HandleFunc("POST /api/sessions/{name}/marks", rest)
	mux.HandleFunc("DELETE /api/sessions/{name}/marks", rest)
	// Auto session-title suggestion accept/dismiss (session_title.go, Agent-side).
	mux.HandleFunc("POST /api/sessions/{name}/title/accept", rest)
	mux.HandleFunc("POST /api/sessions/{name}/title/dismiss", rest)
	mux.HandleFunc("POST /api/sessions/{name}/title/suggest", rest)
	mux.HandleFunc("POST /api/sessions/{name}/title/set", rest)
	mux.HandleFunc("POST /api/sessions/{name}/suggest-branch", rest)  // LLM branch-name suggestion (this session's convo)
	mux.HandleFunc("POST /api/sessions/{name}/suggest-replies", rest) // LLM reply suggestion v2 (this session's convo)
	mux.HandleFunc("GET /api/sessions/{name}/skills", rest)           // mirror skill picker (docs/log/50 / ADR0034)
	mux.HandleFunc("POST /api/sessions/{name}/rename-branch", rest)   // worktree deferred-naming: git branch -m
}

func registerSessionShareRoutes(mux *http.ServeMux, cfg config) {
	a := newSessionShareAPI(cfg.mgr)
	mux.HandleFunc("GET /api/session-shares", a.withMembership(a.listOwned))
	mux.HandleFunc("GET /api/session-share-recipients", a.withMembership(a.searchRecipients))
	mux.HandleFunc("POST /api/session-shares", a.withResolved(a.put))
	mux.HandleFunc("PATCH /api/session-shares/{id}", a.withMembership(a.patch))
	mux.HandleFunc("DELETE /api/session-shares/{id}", a.withMembership(a.delete))
	mux.HandleFunc("GET /api/shared-sessions", a.withMembership(a.listReceived))
	mux.HandleFunc("GET /api/shared-sessions/{id}/messages", a.withMembership(a.messages))
	mux.HandleFunc("GET /api/shared-sessions/{id}/handoff-proposals", a.withMembership(a.handoffProposals))
	// Transcript marks (docs/log/69 / ADR 0050). RO may read, only RW may write — and a
	// write does not go through "propose -> owner approves", because an annotation does
	// not drive the agent.
	mux.HandleFunc("GET /api/shared-sessions/{id}/marks", a.withMembership(a.marks))
	mux.HandleFunc("POST /api/shared-sessions/{id}/marks", a.withMembership(a.marks))
	mux.HandleFunc("DELETE /api/shared-sessions/{id}/marks", a.withMembership(a.marks))
	mux.HandleFunc("POST /api/shared-sessions/{id}/proposals", a.withMembership(a.propose))
	mux.HandleFunc("GET /api/session-share-proposals", a.withMembership(a.listProposals))
	mux.HandleFunc("POST /api/session-share-proposals/{id}/approve", a.withMembership(a.approve))
	mux.HandleFunc("POST /api/session-share-proposals/{id}/reject", a.withMembership(a.reject))

	// Handoff to another member (docs/log/77 / ADR 0057). It derives from the share ACL,
	// so it lives here and shares the inventory-sync throttle with the same instance of
	// the share API.
	h := newSessionHandoffAPI(cfg.mgr, a)
	mux.HandleFunc("GET /api/sessions/{name}/handoff-recipients", h.withResolved(h.recipients))
	mux.HandleFunc("POST /api/session-handoff-offers", h.withResolved(h.create))
	mux.HandleFunc("GET /api/session-handoff-offers", h.withMembership(h.listOwned))
	// The recipient's inbox. A literal segment outranks {id}, so this ordering does not
	// collide.
	mux.HandleFunc("GET /api/session-handoff-offers/received", h.withMembership(h.listReceived))
	mux.HandleFunc("DELETE /api/session-handoff-offers/{id}", h.withMembership(h.withdraw))
	mux.HandleFunc("POST /api/session-handoff-offers/{id}/accept", h.withMembership(h.accept))
	mux.HandleFunc("POST /api/session-handoff-offers/{id}/decline", h.withMembership(h.decline))
}

// Assistant chat (docs/log/19) — headless-CLI LLM chat/translation, proxied to the
// Agent verbatim (kind-agnostic; non-streaming, so the plain REST proxy suffices).
func registerChatRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	mux.HandleFunc("GET /api/chat/conversations", rest)
	mux.HandleFunc("POST /api/chat/conversations", rest)
	mux.HandleFunc("GET /api/chat/conversations/{id}", rest)
	mux.HandleFunc("PATCH /api/chat/conversations/{id}", rest)
	mux.HandleFunc("POST /api/chat/conversations/{id}/title/suggest", rest)   // preview-only AI title suggestion (chat_title.go, Agent-side)
	mux.HandleFunc("POST /api/chat/conversations/{id}/suggest-replies", rest) // LLM reply suggestion v2 (chat_suggest_reply.go, Agent-side)
	mux.HandleFunc("DELETE /api/chat/conversations/{id}", rest)
	mux.HandleFunc("POST /api/chat/conversations/{id}/lock", rest) // deletion lock (docs/log/45)
	mux.HandleFunc("POST /api/chat/conversations/{id}/messages", rest)
	mux.HandleFunc("POST /api/chat/conversations/{id}/stream", proxy.withResolved(proxy.stream)) // SSE (Phase B)
	mux.HandleFunc("POST /api/chat/conversations/{id}/stop", rest)                               // cancel a detached in-flight turn
	mux.HandleFunc("POST /api/chat/conversations/{id}/compact", rest)                            // summary carry-forward (docs/log/33 stage 2)
	mux.HandleFunc("GET /api/chat/conversations/{id}/plan", rest)                                // read the work plan (docs/log/33 stage 5)
	mux.HandleFunc("PUT /api/chat/conversations/{id}/plan", rest)                                // hand-edit the work plan (docs/log/33 stage 5)
	mux.HandleFunc("POST /api/chat/conversations/{id}/plan/refresh", rest)                       // explicit work-plan refresh (same)
	mux.HandleFunc("POST /api/chat/conversations/{id}/paste-image", rest)
	mux.HandleFunc("GET /api/chat/conversations/{id}/pasted/{file}", rest)
	// One-shot advisory turn (docs/log/21 memo tidy-up) — stateless, tools off. Proxied verbatim.
	mux.HandleFunc("POST /api/chat/ask", rest)
}

// Assistant templates (docs/log/19 Q2) — configurable chat personas, proxied verbatim.
func registerAssistantRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	mux.HandleFunc("GET /api/assistants", rest)
	mux.HandleFunc("POST /api/assistants", rest)
	mux.HandleFunc("GET /api/assistants/{id}", rest)
	mux.HandleFunc("PUT /api/assistants/{id}", rest)
	mux.HandleFunc("DELETE /api/assistants/{id}", rest)
}

// SSM login config (docs/log/p3-ssm-session.md) — per-member profiles (common
// auth bundle) + host bookmarks (per-instance). No AWS secrets; the aws CLI in the
// workspace authenticates via SSO.
func registerSSMRoutes(mux *http.ServeMux, cfg config) {
	ssm := newSSMConfigAPI(cfg.mgr)
	mux.HandleFunc("GET /api/ssm/profiles", ssm.withMembership(ssm.listProfiles))
	mux.HandleFunc("POST /api/ssm/profiles", ssm.withMembership(ssm.createProfile))
	mux.HandleFunc("PUT /api/ssm/profiles/{id}", ssm.withMembership(ssm.updateProfile))
	mux.HandleFunc("DELETE /api/ssm/profiles/{id}", ssm.withMembership(ssm.deleteProfile))
	mux.HandleFunc("GET /api/ssm/hosts", ssm.withMembership(ssm.listHosts))
	mux.HandleFunc("POST /api/ssm/hosts", ssm.withMembership(ssm.createHost))
	mux.HandleFunc("PUT /api/ssm/hosts/{id}", ssm.withMembership(ssm.updateHost))
	mux.HandleFunc("DELETE /api/ssm/hosts/{id}", ssm.withMembership(ssm.deleteHost))
}

// Work item inbox (docs/log/80) — external tickets in the left rail. The list and the refresh
// button need the Runtime handle (the Agent does the fetching, holding the tokens), so
// they are withResolved; the saved queries and the ledger are pure CP rows and stay on
// withMembership so they work while the Workspace is stopped.
//
// ⚠️ There is deliberately NO agent-proxy entry here: the CP calls the Agent's
// /work-items/fetch itself (like drainAgentOutbox), so the Console never reaches it and
// the allowlist stays out of the picture.
func registerWorkItemRoutes(mux *http.ServeMux, cfg config) {
	wi := newWorkItemsAPI(cfg.mgr)
	mux.HandleFunc("GET /api/work-items", wi.withResolved(wi.list))
	mux.HandleFunc("POST /api/work-items/refresh", wi.withResolved(wi.refresh))
	// Write-back (docs/log/80 §80.10): only once a human has approved the draft. The CP
	// relays it to the Agent.
	mux.HandleFunc("POST /api/work-items/comment", wi.withResolved(wi.comment))
	mux.HandleFunc("GET /api/work-item-queries", wi.withMembership(wi.listQueries))
	mux.HandleFunc("POST /api/work-item-queries", wi.withMembership(wi.createQuery))
	mux.HandleFunc("PATCH /api/work-item-queries/{id}", wi.withMembership(wi.updateQuery))
	mux.HandleFunc("DELETE /api/work-item-queries/{id}", wi.withMembership(wi.deleteQuery))
	mux.HandleFunc("POST /api/work-item-sessions", wi.withMembership(wi.createSession))
	mux.HandleFunc("DELETE /api/work-item-sessions/{id}", wi.withMembership(wi.deleteSession))
}

// Memo queue (docs/log/21) — per-member notes accumulated across devices, then flushed
// to a session as one message. Scoped by membership (no workspace build for CRUD).
func registerMemoRoutes(mux *http.ServeMux, cfg config) {
	memo := newMemoAPI(cfg.mgr)
	mux.HandleFunc("GET /api/memos", memo.withMembership(memo.list))
	mux.HandleFunc("POST /api/memos", memo.withMembership(memo.create))
	mux.HandleFunc("POST /api/memos/flush", memo.withResolved(memo.flush))
	mux.HandleFunc("PATCH /api/memos/{id}", memo.withMembership(memo.update))
	mux.HandleFunc("DELETE /api/memos/{id}", memo.withMembership(memo.delete))

	// Memo image attachments (docs/log/21 image attachments) — upload/serve/GC proxied verbatim to the
	// workspace Agent (/api stripped -> /memos/*), which stores the bytes per-container.
	// These are more specific than POST /api/memos so the mux routes them to the proxy.
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	mux.HandleFunc("POST /api/memos/paste-image", rest)
	mux.HandleFunc("GET /api/memos/images/{file}", rest)
	mux.HandleFunc("POST /api/memos/images/gc", rest)

	// First-class categories (docs/log/21 UI overhaul): add empty, rename (cascades), reorder.
	mux.HandleFunc("GET /api/memo-categories", memo.withMembership(memo.listCategories))
	mux.HandleFunc("POST /api/memo-categories", memo.withMembership(memo.createCategory))
	mux.HandleFunc("PATCH /api/memo-categories/{id}", memo.withMembership(memo.updateCategory))
	mux.HandleFunc("DELETE /api/memo-categories/{id}", memo.withMembership(memo.deleteCategory))

	// Internal (operator-token) face: an in-container fleet operator has no
	// gateway session, so it hits these with its AF_MEMO_TOKEN Bearer (memo_bridge.go).
	// /internal/* is session-exempt; auth + membership scoping live in withMemoToken.
	mux.HandleFunc("GET /internal/memos", memo.withMemoToken(memo.list))
	mux.HandleFunc("POST /internal/memos", memo.withMemoToken(memo.create))
	mux.HandleFunc("POST /internal/memos/flush", memo.withMemoTokenResolved(memo.flush))
	mux.HandleFunc("PATCH /internal/memos/{id}", memo.withMemoToken(memo.update))
	mux.HandleFunc("DELETE /internal/memos/{id}", memo.withMemoToken(memo.delete))
	mux.HandleFunc("GET /internal/memo-categories", memo.withMemoToken(memo.listCategories))
	mux.HandleFunc("POST /internal/memo-categories", memo.withMemoToken(memo.createCategory))
	mux.HandleFunc("PATCH /internal/memo-categories/{id}", memo.withMemoToken(memo.updateCategory))
	mux.HandleFunc("DELETE /internal/memo-categories/{id}", memo.withMemoToken(memo.deleteCategory))
}

// Scheduled execution (docs/log/38 + ADR0021) — operator-authored cron/interval/once tasks.
// Definitions live in the CP DB; the scheduler goroutine (scheduler.go) drives them.
// Two faces share the same membership-scoped scheduleAPI handlers:
//   - /internal/* (operator token, session-exempt): the operator MCP writes here via its
//     AF_SCHEDULE_TOKEN Bearer (schedule_bridge.go). Full CRUD incl. create/update, whose
//     NL->spec translation is the operator LLM's job.
//   - /api/schedules* (gateway member auth, P5 Console GUI): read + manage only (list /
//     runs / pause / resume / run-now / delete). Create/edit stay operator-only because a
//     schedule is authored from natural language the operator translates to a cron spec.
//
// The member handlers take (w, r, mv); scheduleMember adapts withMembership's (id, mv) form.
func registerScheduleRoutes(mux *http.ServeMux, cfg config) {
	s := newScheduleAPI(cfg.mgr)
	mux.HandleFunc("GET /internal/schedules", s.withScheduleToken(s.list))
	mux.HandleFunc("POST /internal/schedules", s.withScheduleToken(s.create))
	mux.HandleFunc("PATCH /internal/schedules/{id}", s.withScheduleToken(s.update))
	mux.HandleFunc("DELETE /internal/schedules/{id}", s.withScheduleToken(s.delete))
	mux.HandleFunc("POST /internal/schedules/{id}/pause", s.withScheduleToken(s.pause))
	mux.HandleFunc("POST /internal/schedules/{id}/resume", s.withScheduleToken(s.resume))
	mux.HandleFunc("POST /internal/schedules/{id}/run-now", s.withScheduleToken(s.runNow))
	mux.HandleFunc("GET /internal/schedules/{id}/runs", s.withScheduleToken(s.runs))

	// Console member routes (P5): the logged-in member manages their own schedules. No
	// create here — authoring a schedule from natural language is the operator's NL->spec
	// translation. update IS exposed (P5.2 detail/edit modal): the Console form edits the
	// STRUCTURED fields directly (spec_kind/spec/tz/label/prompt/wake/agent/model), which
	// needs no NL translation — the same structured patch the operator's update tool sends.
	mux.HandleFunc("GET /api/schedules", scheduleMember(s, s.list))
	mux.HandleFunc("GET /api/schedules/{id}/runs", scheduleMember(s, s.runs))
	mux.HandleFunc("PATCH /api/schedules/{id}", scheduleMember(s, s.update))
	mux.HandleFunc("POST /api/schedules/{id}/pause", scheduleMember(s, s.pause))
	mux.HandleFunc("POST /api/schedules/{id}/resume", scheduleMember(s, s.resume))
	mux.HandleFunc("POST /api/schedules/{id}/run-now", scheduleMember(s, s.runNow))
	mux.HandleFunc("DELETE /api/schedules/{id}", scheduleMember(s, s.delete))
}

// scheduleMember wraps a membership-scoped schedule handler in the gateway member auth so
// the Console can reach it, dropping the Identity that withMembership also resolves (the
// schedule handlers key everything off mv.MembershipID).
func scheduleMember(s scheduleAPI, h func(http.ResponseWriter, *http.Request, store.MembershipView)) http.HandlerFunc {
	return s.withMembership(func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		h(w, r, mv)
	})
}

// Tenant-distributed MCP servers (docs/log/48 P4 + ADR0031) — the definitions a
// tenant_admin distributes to every member of a tenant. Two faces over one table
// (mcp_server.go):
//
//   - /api/admin/mcp-servers*  Console admin modal, gated per-tenant mid-handler
//     (tenantAdminFor: super_admin any tenant, tenant_admin their own). Audited.
//   - GET /internal/mcp-servers  the Workspace agent's poll, authenticated by the
//     per-membership AF_MCP_TOKEN (mcp_server_bridge.go) and session-exempt via the
//     /internal/ prefix already declared by registerTenantAdminRoutes.
//
// The member-facing registry (/api/mcp-servers) is NOT here — it is proxied to the
// Agent by registerConnectionRoutes, because the effective registry is composed inside
// the workspace where the user's own encrypted store lives.
func registerMCPServerRoutes(mux *http.ServeMux, cfg config) {
	exemptPrefix("/internal/")
	m := mcpsrv.NewServerAPI(cpDeps{cfg.mgr})
	mux.HandleFunc("GET /api/admin/mcp-servers", m.AdminList)
	mux.HandleFunc("POST /api/admin/mcp-servers", m.AdminUpsert)
	mux.HandleFunc("PUT /api/admin/mcp-servers/{id}", m.AdminUpsert)
	mux.HandleFunc("DELETE /api/admin/mcp-servers/{id}", m.AdminDelete)
	mux.HandleFunc("GET /internal/mcp-servers", m.WithMCPToken(m.Distribute))
	// Role-scoped docs pull (docs/build/04 §4.9). Same shape as the MCP poll — a
	// per-membership token, session-exempt via /internal/ — and the only way an ECS
	// workspace gets its docs at all, since there is no host path to bind-mount there.
	mux.HandleFunc("GET /internal/docs", newDocsAPI(cfg.mgr).download)
}

// Repository ops + source-control view + file browser — proxied to the Workspace
// Agent (/api stripped -> /repos*, /git/identity, /fs/*).
func registerRepoFSRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	mux.HandleFunc("GET /api/repos", rest)
	mux.HandleFunc("POST /api/repos", rest)
	mux.HandleFunc("POST /api/repos/init", rest) // new working copy with no import source (mkdir + git init)
	mux.HandleFunc("DELETE /api/repos/{name}", rest)
	mux.HandleFunc("POST /api/repos/{name}/lock", rest) // deletion lock (docs/log/45)
	mux.HandleFunc("GET /api/repos/{name}/status", rest)
	mux.HandleFunc("GET /api/repos/{name}/branches", rest)
	mux.HandleFunc("DELETE /api/repos/{name}/branch", rest) // ?branch=<name>; cleanup (docs/log/32)
	mux.HandleFunc("POST /api/repos/{name}/checkout", rest)
	mux.HandleFunc("POST /api/repos/{name}/fetch", rest)
	mux.HandleFunc("POST /api/repos/{name}/ff", rest)
	mux.HandleFunc("POST /api/repos/{name}/parent-ff", rest)
	// Repository import jobs (docs/log/78) — progress and outcome of a clone / svn checkout.
	mux.HandleFunc("GET /api/repo-jobs", rest)
	mux.HandleFunc("DELETE /api/repo-jobs/{id}", rest)
	// Subversion (docs/log/41) — checkout / update / cleanup, proxied to the Agent.
	mux.HandleFunc("POST /api/repos/svn", rest)
	mux.HandleFunc("POST /api/repos/{name}/svn-update", rest)
	mux.HandleFunc("POST /api/repos/{name}/svn-cleanup", rest)
	// Launch prompt templates (repo launch modal) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/prompt-templates", rest)
	// Project-scope MCP servers (docs/log/56 P0/P1) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/mcp", rest)
	mux.HandleFunc("POST /api/repos/{name}/mcp/plan", rest)
	mux.HandleFunc("POST /api/repos/{name}/mcp/apply", rest)
	// Source-control view + light edits (docs/17 P3-5) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/changes", rest)
	mux.HandleFunc("GET /api/repos/{name}/diff", rest)
	mux.HandleFunc("GET /api/repos/{name}/log", rest)
	mux.HandleFunc("GET /api/repos/{name}/graph", rest)
	mux.HandleFunc("GET /api/repos/{name}/show", rest)
	mux.HandleFunc("POST /api/repos/{name}/stage", rest)
	mux.HandleFunc("POST /api/repos/{name}/unstage", rest)
	mux.HandleFunc("POST /api/repos/{name}/discard", rest)
	mux.HandleFunc("POST /api/repos/{name}/commit", rest)
	mux.HandleFunc("GET /api/repos/{name}/identity", rest)
	mux.HandleFunc("PUT /api/repos/{name}/identity", rest)
	mux.HandleFunc("GET /api/git/identity", rest)
	mux.HandleFunc("PUT /api/git/identity", rest)
	// File browser (docs/17 P3-5 stage 2) — proxied to the Agent.
	mux.HandleFunc("GET /api/fs/tree", rest)
	mux.HandleFunc("GET /api/fs/search", rest)
	mux.HandleFunc("GET /api/fs/file", rest)
	mux.HandleFunc("PUT /api/fs/file", proxy.withResolved(proxy.fsFilePut))
	// Resolves path references in mirror text — read-only, stat only
	// (workspace/agent/fs_resolve.go).
	mux.HandleFunc("POST /api/fs/resolve", rest)
	// The editor's AI edit suggestion (docs/log/44 Phase 4) — read-only generation, not
	// audited.
	mux.HandleFunc("POST /api/fs/suggest-edit", rest)
	mux.HandleFunc("GET /api/fs/download", rest)
	mux.HandleFunc("POST /api/fs/upload", rest)
	mux.HandleFunc("GET /api/fs/changes", rest)
	mux.HandleFunc("GET /api/fs/linemarks", rest)
	mux.HandleFunc("POST /api/fs/mkdir", rest)
	mux.HandleFunc("POST /api/fs/newfile", rest)
	mux.HandleFunc("POST /api/fs/rename", rest)
	mux.HandleFunc("DELETE /api/fs/delete", rest)
}

// Per-CLI settings/usage + toolchains + UI prefs — mostly proxied to the Agent;
// ws-settings is CP-owned (editable while stopped; applied at start).
func registerAgentEnvRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	// Claude settings (Remote Control / notifications / RTK) — proxied to the Agent.
	mux.HandleFunc("GET /api/claude/settings", rest)
	mux.HandleFunc("PUT /api/claude/settings", rest)
	mux.HandleFunc("GET /api/claude/usage", rest)
	mux.HandleFunc("GET /api/codex/usage", rest)
	mux.HandleFunc("GET /api/codex/settings", rest)
	mux.HandleFunc("PUT /api/codex/settings", rest)
	// Copilot account credit usage (WsBar chip) — proxied to the Agent.
	mux.HandleFunc("GET /api/copilot/usage", rest)
	// codex / opencode rtk toggle — proxied to the Agent.
	mux.HandleFunc("GET /api/agents/rtk", rest)
	mux.HandleFunc("PUT /api/agents/rtk", rest)
	// rtk token-savings history (WsBar "rtk 効果" chip) — proxied to the Agent.
	mux.HandleFunc("GET /api/agents/rtk/gain", rest)
	// User instructions (docs/log/60 / ADR 0042) — the source of truth and its
	// destinations are both inside the container, so this is only an Agent relay.
	mux.HandleFunc("GET /api/user-notes", rest)
	mux.HandleFunc("PUT /api/user-notes", rest)
	mux.HandleFunc("GET /api/user-notes/preview", rest)
	// codex / opencode model catalogs (launch model picker) — proxied to the Agent.
	mux.HandleFunc("GET /api/agents/{kind}/models", rest)
	// Agent memory version control (docs/log/39 / ADR 0022 P1-P3) — a relay for work that
	// completes inside the Agent. The last segment differs from {kind}/models, so the
	// patterns do not collide.
	mux.HandleFunc("GET /api/agents/memory/roots", rest)
	mux.HandleFunc("GET /api/agents/memory/snapshots", rest)
	mux.HandleFunc("POST /api/agents/memory/snapshots", rest)
	mux.HandleFunc("GET /api/agents/memory/diff", rest)
	mux.HandleFunc("GET /api/agents/memory/tree", rest)
	mux.HandleFunc("POST /api/agents/memory/restore", rest)
	mux.HandleFunc("PUT /api/agents/memory/settings", rest)
	// Transfer between environments (P3). export streams the body with its
	// Content-Disposition untouched and import hands the multipart straight to the
	// Agent; rest passes body and headers through.
	mux.HandleFunc("GET /api/agents/memory/export", rest)
	mux.HandleFunc("POST /api/agents/memory/import", rest)
	mux.HandleFunc("POST /api/agents/memory/import/apply", rest)
	// Toolchain selection (node / java) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/toolchains", rest)
	mux.HandleFunc("PUT /api/env/toolchains", rest)
	// One-button Temurin install for the Java row (agent jdk_install_http.go): POST
	// starts the download in the workspace, GET polls its state.
	mux.HandleFunc("POST /api/env/jdk-install", rest)
	mux.HandleFunc("GET /api/env/jdk-install", rest)
	mux.HandleFunc("POST /api/env/node-install", rest)
	mux.HandleFunc("GET /api/env/node-install", rest)
	mux.HandleFunc("GET /api/env/tool-versions", rest)
	// CP-owned per-workspace settings (editable while stopped; applied at start).
	wss := newWSSettingsAPI(cfg.mgr)
	mux.HandleFunc("GET /api/env/ws-settings", wss.withResolved(wss.get))
	mux.HandleFunc("PUT /api/env/ws-settings", wss.withResolved(wss.put))
	// Reissue the preview URL (docs/log/81 §4.1). POST rather than GET/PUT because it
	// throws state away.
	mux.HandleFunc("POST /api/env/ws-settings/preview/reissue", wss.withResolved(wss.reissuePreview))
	// Previews shared inside the same tenant (docs/log/81 §14.6). withMembership, not
	// withResolved: this reads another Workspace's publication settings, so the caller's
	// own container is not needed and the answer is available while it is stopped.
	mux.HandleFunc("GET /api/preview/shared", wss.withMembership(wss.sharedPreviews))
	// Per-user UI preferences (Console display settings) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/ui-prefs", rest)
	mux.HandleFunc("PUT /api/env/ui-prefs", rest)
}

// Connections ops — proxied to the Workspace Agent (/api stripped), except the
// Bitbucket OAuth code grant whose public callback the CP owns.
func registerConnectionRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	rest := proxy.withResolved(proxy.rest)
	// restLogin gates the start/poll/complete legs of an agent-CLI login flow on
	// the workspace being fully rolled out (see restLoginFlow) — their state lives
	// only in the Agent process's memory, so a wake-triggered task swap mid-flow
	// silently drops it otherwise.
	restLogin := proxy.withResolved(proxy.restLoginFlow)
	// MCP registry (docs/log/48 P0) — the implementation is Agent-side
	// (workspace/agent/mcp_servers.go).
	mux.HandleFunc("GET /api/mcp-servers", rest)
	mux.HandleFunc("POST /api/mcp-servers", rest)
	mux.HandleFunc("POST /api/mcp-servers/test", rest)
	mux.HandleFunc("PUT /api/mcp-servers/{id}", rest)
	mux.HandleFunc("POST /api/mcp-servers/{id}/enabled", rest)
	mux.HandleFunc("DELETE /api/mcp-servers/{id}", rest)
	// P4: the member's half of tenant distribution — pull the tenant set now (instead of
	// waiting for the poll), and fill in the values of a user_secret server's headers.
	mux.HandleFunc("POST /api/mcp-servers/tenant-refresh", rest)
	mux.HandleFunc("PUT /api/mcp-servers/{id}/secrets", rest)
	mux.HandleFunc("GET /api/connections", rest)
	mux.HandleFunc("GET /api/connections/git/{host}/repos", rest)
	mux.HandleFunc("GET /api/connections/git/{host}/branches", rest)
	mux.HandleFunc("PUT /api/connections/git/{host}", rest)
	mux.HandleFunc("PUT /api/connections/git/{host}/identity", rest)
	mux.HandleFunc("DELETE /api/connections/git/{host}", rest)
	// Git provider OAuth — CP-native for BOTH providers since docs/log/71: the OAuth app is
	// the tenant's, and the tenant's row lives in the CP's database. GitHub's device
	// flow used to be proxied to the Agent (restLogin), which is why the paths keep
	// their shape — the Console calls exactly the same two endpoints.
	mux.HandleFunc("POST /api/connections/git/github/oauth/start", cfg.handleGithubDeviceStart)
	mux.HandleFunc("POST /api/connections/git/github/oauth/poll", cfg.handleGithubDevicePoll)
	mux.HandleFunc("GET /api/connections/git/bitbucket/oauth/start", cfg.handleBitbucketOAuthStart)
	mux.HandleFunc("GET /api/oauth/bitbucket/callback", cfg.handleBitbucketOAuthCallback)
	mux.HandleFunc("GET /api/oauth/jira/callback", cfg.handleJiraOAuthCallback)
	// Which OAuth buttons this member's tenant can offer (docs/log/71 §71.4). CP-native and
	// separate from GET /api/connections on purpose: that one is proxied to the Agent
	// and answers 502 while the workspace is stopped, which is exactly when this tab is
	// being looked at.
	gitOAuth := newTenantGitOAuthAPI(cfg.mgr)
	mux.HandleFunc("GET /api/git-oauth", gitOAuth.withMembership(gitOAuth.availability))
	// The Agent's refresh bridge (docs/log/71 §71.8): the tenant's client secret stays in the
	// CP, so the workspace posts its refresh token here instead of holding the secret.
	// Session-exempt via the /internal/ prefix; authenticated by AF_GIT_OAUTH_TOKEN.
	exemptPrefix("/internal/")
	gob := newGitOAuthBridgeAPI(cfg.mgr)
	mux.HandleFunc("POST /internal/git-oauth/bitbucket/refresh", gob.withGitOAuthToken(gob.refreshBitbucket))
	mux.HandleFunc("POST /internal/git-oauth/jira/refresh", gob.withGitOAuthToken(gob.refreshJira))
	mux.HandleFunc("POST /api/connections/claude/start", restLogin)
	mux.HandleFunc("POST /api/connections/claude/complete", restLogin)
	mux.HandleFunc("DELETE /api/connections/claude", rest)
	// agy (Antigravity CLI, docs/log/32) — claude-style flow: start returns the
	// authorize URL (+ flow_id; body carries method: oauth|gcp-project — M1
	// implements oauth only), complete submits the pasted code. usage feeds the
	// AgyCard's Starter-Quota gauge (TUI /usage scrape, agent-side).
	mux.HandleFunc("POST /api/connections/agy/start", restLogin)
	mux.HandleFunc("POST /api/connections/agy/complete", restLogin)
	mux.HandleFunc("DELETE /api/connections/agy", rest)
	mux.HandleFunc("GET /api/connections/agy/usage", rest)
	// cursor (Cursor CLI, docs/log/40) — dedicated login flow: start returns the
	// authorize URL (+ flow_id), poll checks browser approval (no pasted code —
	// cursor self-polls). Proxied to the Agent (cursor owns ~/.config/cursor/auth.json).
	mux.HandleFunc("POST /api/connections/cursor/start", restLogin)
	mux.HandleFunc("POST /api/connections/cursor/poll", restLogin)
	mux.HandleFunc("DELETE /api/connections/cursor", rest)
	// kiro (Kiro CLI, docs/log/43) — dedicated device-flow login: start returns the
	// verification URL (+ user_code + flow_id), poll checks AWS-side approval (kiro
	// self-polls, no pasted code). Proxied to the Agent (kiro owns its credential
	// store under ~/.local/share/kiro-cli).
	mux.HandleFunc("POST /api/connections/kiro/start", restLogin)
	mux.HandleFunc("POST /api/connections/kiro/poll", restLogin)
	mux.HandleFunc("DELETE /api/connections/kiro", rest)
	// kiro on-demand install (docs/log/43 Track B/C) — the ~855MB bundle is not baked on
	// the lean image, so the connection card triggers a background install (POST) and
	// polls its progress (GET). Proxied to the Agent (installs into the user's ~/.local).
	mux.HandleFunc("POST /api/connections/kiro/install", rest)
	mux.HandleFunc("GET /api/connections/kiro/install", rest)
	mux.HandleFunc("PUT /api/connections/opencode", rest)
	mux.HandleFunc("DELETE /api/connections/opencode/{env}", rest)
	mux.HandleFunc("POST /api/connections/opencode/oauth/start", restLogin)
	mux.HandleFunc("POST /api/connections/opencode/oauth/poll", restLogin)
	mux.HandleFunc("POST /api/connections/opencode/oauth/cancel", rest)
	mux.HandleFunc("DELETE /api/connections/opencode/oauth", rest)
	mux.HandleFunc("PUT /api/connections/opencode/workspace", rest)
	// SVN saved basic-auth creds (docs/log/41) — forget a stored server credential.
	mux.HandleFunc("DELETE /api/connections/svn", rest)
	// Codex auth — proxied to the Agent (codex owns auth.json; no public callback,
	// device-auth polls OpenAI from inside the container).
	mux.HandleFunc("POST /api/connections/codex/api-key", rest)
	mux.HandleFunc("POST /api/connections/codex/device/start", restLogin)
	mux.HandleFunc("POST /api/connections/codex/device/poll", restLogin)
	mux.HandleFunc("DELETE /api/connections/codex", rest)
	// Ops connections (docs/log/25 Phase 1): the credentials are stored in the
	// Workspace's encrypted secrets and injected into the ops MCP servers at
	// spawn; the CP only proxies here, never holds the secrets.
	// Jira (docs/log/80 P1) — where work items are fetched from. It rides along on GET
	// /api/connections, but save and delete each need their own line here: the CP is an
	// explicit allowlist, not a catch-all.
	mux.HandleFunc("PUT /api/connections/jira", rest)
	mux.HandleFunc("DELETE /api/connections/jira", rest)
	// OAuth (docs/log/80 §80.17): the CP builds the authorize URL on start, then receives
	// the callback and passes the token to the Agent (same shape as Bitbucket,
	// oauth_jira.go). Only the site choice is a Console -> Agent proxy.
	mux.HandleFunc("POST /api/connections/jira/oauth/start", cfg.handleJiraOAuthStart)
	mux.HandleFunc("PUT /api/connections/jira/site", rest)
	mux.HandleFunc("PUT /api/connections/pagerduty", rest)
	mux.HandleFunc("DELETE /api/connections/pagerduty", rest)
	mux.HandleFunc("PUT /api/connections/grafana", rest)
	mux.HandleFunc("DELETE /api/connections/grafana", rest)
	mux.HandleFunc("PUT /api/connections/cloudwatch", rest)
	mux.HandleFunc("DELETE /api/connections/cloudwatch", rest)
	mux.HandleFunc("PUT /api/connections/aws", rest)
	mux.HandleFunc("DELETE /api/connections/aws", rest)
	// Chat bridge (docs/log/37 P1): the Discord bot token lives in the Workspace's
	// encrypted secrets like the ops credentials above — proxied, never held here.
	mux.HandleFunc("PUT /api/connections/discord", rest)
	mux.HandleFunc("DELETE /api/connections/discord", rest)
	mux.HandleFunc("POST /api/connections/discord/inspect", rest)
	mux.HandleFunc("POST /api/connections/discord/guilds", rest)
	// Chat bridge Slack (docs/log/37, Slack follow-up): bot + app-level tokens live in the
	// Workspace's encrypted secrets like Discord above — proxied, never held here.
	mux.HandleFunc("PUT /api/connections/slack", rest)
	mux.HandleFunc("DELETE /api/connections/slack", rest)
	mux.HandleFunc("POST /api/connections/slack/inspect", rest)
	mux.HandleFunc("POST /api/connections/slack/channels", rest)
}

// Internal git provider (docs/reference/internal-git-provider, ADR 0010).
// Repo management is CP-native (the CP owns the bare repos), so these are NOT
// proxied to the Agent like other providers. /git/* self-authenticates via a
// Basic git token — session-exempt.
func registerInternalGitRoutes(mux *http.ServeMux, cfg config) {
	exemptPrefix("/git/")
	g := newGitServerAPI(cfg.mgr, cfg.publicBaseURL)
	mux.HandleFunc("GET /api/internal-git/repos", g.withMembership(g.reposList))
	mux.HandleFunc("POST /api/internal-git/repos", g.withMembership(g.repoCreate))
	mux.HandleFunc("DELETE /api/internal-git/repos/{name}", g.withMembership(g.repoDelete))
	mux.HandleFunc("POST /api/internal-git/repos/{name}/rename", g.withMembership(g.repoRename))
	mux.HandleFunc("GET /api/internal-git/repos/{name}/branches", g.withMembership(g.branches))
	// Read-only browsing (clone-free): tree / blob / commits, served from the bare.
	mux.HandleFunc("GET /api/internal-git/repos/{name}/tree", g.withMembership(g.tree))
	mux.HandleFunc("GET /api/internal-git/repos/{name}/blob", g.withMembership(g.blob))
	mux.HandleFunc("GET /api/internal-git/repos/{name}/commits", g.withMembership(g.commits))
	// Git LFS face (docs/reference/internal-git-provider, P3). More specific than the
	// smart-HTTP catch-all below, so these win for LFS paths; git-http-backend never
	// sees them. Same Basic git-token auth (session-exempt under /git/).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/objects/batch", g.lfsBatch)
	mux.HandleFunc("PUT /git/{slug}/{repo}/info/lfs/objects/{oid}", g.lfsUpload)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/objects/{oid}", g.lfsDownload)
	// LFS file locking API (create / list / verify / unlock).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks", g.lfsLockCreate)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/locks", g.lfsLocksList)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/verify", g.lfsLocksVerify)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/{id}/unlock", g.lfsUnlock)
	// Smart-HTTP git face (clone/fetch/push). Self-authenticating via a Basic git
	// token (session-exempt, like /mcp); handles every method.
	mux.HandleFunc("/git/{slug}/{repo...}", g.gitHTTP)
}

// Browser Page lifecycle + restricted rendering/input WebSocket (docs/log/31).
// The browser implementation remains separate from terminal relay semantics:
// binary JPEG frames are replaceable under backpressure, PTY bytes are not.
func registerBrowserRoutes(mux *http.ServeMux, cfg config) {
	browser := newBrowserAPI(cfg.mgr)
	rest := browser.withResolved(browser.rest)
	mux.HandleFunc("POST /api/browser/pages", rest)
	mux.HandleFunc("GET /api/browser/pages/{id}", rest)
	mux.HandleFunc("DELETE /api/browser/pages/{id}", rest)
	mux.HandleFunc("GET /api/browser/attach-targets", rest)
	mux.HandleFunc("POST /api/browser/attachments", rest)
	mux.HandleFunc("GET /api/browser/attachments", rest)
	mux.HandleFunc("GET /api/browser/attachments/{id}", rest)
	mux.HandleFunc("DELETE /api/browser/attachments/{id}", rest)
	mux.HandleFunc("POST /api/browser/attachments/{id}/control-mode", rest)
	mux.HandleFunc("GET /api/browser/attachments/{id}/targets", rest)
	mux.HandleFunc("POST /api/browser/attachments/{id}/retarget", rest)
	mux.HandleFunc("POST /api/browser/attachments/{id}/handoff", rest)
	mux.HandleFunc("POST /api/browser/attachments/{id}/handoff-result", rest)
	mux.HandleFunc("GET /ws/browser", browser.withResolved(browser.socket))
	mux.HandleFunc("GET /ws/browser-attachments", browser.withResolved(browser.attachmentSocket))
}

// Terminal PTY (proxied WebSocket) + preview proxy to a service the user started
// inside their container (Spring Boot, dev server, ...) via the Agent's
// /proxy/{port}. The redirect adds the trailing slash so the app resolves
// relative assets under the path.
func registerTerminalPreviewRoutes(mux *http.ServeMux, cfg config) {
	proxy := newAgentProxyAPI(cfg.mgr)
	pv := newPreviewAPI(cfg.mgr, cfg.publicBaseURL)
	mux.HandleFunc("GET /ws/terminal", proxy.withResolved(proxy.terminal))
	mux.HandleFunc("/preview/{port}", pv.redirect)
	mux.HandleFunc("/preview/{port}/{rest...}", pv.withPreviewResolved(pv.proxy))
	// Console-origin half of the preview-subdomain handshake (docs/log/81 §6). It lives
	// HERE, on the authenticated origin, precisely so an unauthenticated visitor meets
	// the normal login before any token exists.
	mux.HandleFunc("GET "+previewHandshakePath, newPreviewHostAPI(cfg).handshake)
	// A pasteable link that stays valid across restarts (docs/log/81 §14.6). This one too
	// must sit on the Console origin, inside authGate, so a logged-out visitor meets the
	// login first.
	mux.HandleFunc("GET "+previewOpenPath, newPreviewHostAPI(cfg).openStable)
}

// Legacy path compatibility: the deployment used to be served under
// /agent-fleet (oauth2-proxy + Caddy stripped it). Now it's at the root, so
// old bookmarks — and any stale post-login next=/agent-fleet/… — would 404.
// Redirect /agent-fleet[/…] -> /… (auth-exempt, so it fires before login and
// the dead prefix never reaches next=).
func registerLegacyRedirect(mux *http.ServeMux) {
	exemptExact("/agent-fleet")
	exemptPrefix("/agent-fleet/")
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
}

// Static Console (catch-all). Vite emits content-hashed files under assets/ —
// a rebuild changes the name, never the bytes — so those are safe to cache
// long-term (the mobile win: no multi-MB bundle re-download per open). The
// unhashed entry points (index.html, version.json, sw.js) stay no-store so a
// reload / the deploy check always sees the latest build.
func registerStatic(mux *http.ServeMux, cfg config) {
	// Console action route (docs/log/53 §53.7). The Console is a static SPA, so no
	// file exists at this path — under the catch-all FileServer alone the very
	// link attach_chromium tells the agent to hand the user 404s. Serve the same
	// shell: index.html re-points its dynamic <base> at the Workspace root and
	// App.tsx turns the id into a pane. It stays session-gated like "/", so a
	// logged-out deep link round-trips through /login?next=… and lands here.
	shell := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(cfg.consoleDir, "index.html"))
	}
	mux.HandleFunc("GET /open/browser-attachment/{id}", shell)
	mux.HandleFunc("GET /open/browser-attachment/{id}/{$}", shell)

	fs := http.FileServer(http.Dir(cfg.consoleDir))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		fs.ServeHTTP(w, r)
	}))
}
