package tenantsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Tenants serves the caller-facing tenant picker: the CP's resolution plus a narrow
// TenantStore view, wrapped in withIdentity where the routes are registered. Its counterpart
// on the CP side is tenantAPI (control-plane/tenant_wiring.go), which embeds memberAuth.
type Tenants struct {
	cp    CP
	store store.TenantStore
}

func NewTenants(cp CP, ts store.TenantStore) Tenants { return Tenants{cp, ts} }

// List (GET /api/tenants) returns the caller's memberships for the Console
// tenant picker (docs/14 P3-2). Single-membership users get one entry, so the
// Console can auto-select and hide the picker.
func (a Tenants) List(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	isSuper := ident.Role == "super_admin"
	ms, aerr := a.cp.MembershipsFor(r.Context(), ident)
	if aerr != nil {
		// A super_admin with no membership still gets an answer (docs/log/61 §61.10.2
		// + decision 23). Otherwise bootstrapping a deployment with
		// AF_PROVISION=invite dead-ends right here: this endpoint 403s, the Console's
		// error branch leaves superAdmin false, and the admin menu — whose condition
		// is `superAdmin || tenant_admin` — never appears, so the one person entitled
		// to create the first tenant cannot reach the screen that creates it. Only
		// the UI is blocked (the admin API gates on identityFor and stays reachable),
		// but that is not a procedure anyone can follow.
		if aerr.Code == "not_provisioned" && isSuper {
			writeJSON(w, http.StatusOK, map[string]any{"tenants": []any{}, "super_admin": true})
			return
		}
		writeAPIErr(w, aerr)
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		// allow_agent_self_update: the operator gate, surfaced so the Console can
		// show/hide the member's "keep CLIs updated" toggle for this tenant.
		allowUpd := false
		var provs []string
		if t, err := a.store.GetTenant(r.Context(), m.TenantID); err == nil {
			allowUpd = a.cp.ParseLimits(t.Limits).AllowAgentSelfUpdate
			provs = a.cp.SplitCSVLower(t.AllowedProviders)
		}
		out = append(out, map[string]any{
			"slug": m.TenantSlug, "name": m.TenantName, "role": m.Role,
			"allow_agent_self_update": allowUpd,
			// Which sign-in methods this tenant accepts (docs/log/61 §61.9.4; empty = any).
			// The Console needs it to turn a provider_required refusal into a
			// re-sign-in link instead of a dead end.
			"allowed_providers": provs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "super_admin": isSuper})
}

// Admin is the tenant/membership admin handler set: tenant
// CRUD, memberships, quotas, the tenant's own network rule and machine class. The
// handlers span most sub-stores (tenant / identity / membership / workspace /
// quota / usage / audit / session index), so no narrow view — they reach the
// full store via a.cp.Store(). Handlers gated up-front register through
// withSuperAdmin; per-tenant ones gate mid-handler via CP.TenantAdminFor
// (slug comes from the path on some routes and the body on others).
//
// The CP's own adminAPI (control-plane/tenant_wiring.go) carries more than this:
// the deployment-wide admin views (usage.go, audit.go, admin_sessions.go,
// admin_stats.go, metrics.go hostStats, cloudcost.go) are still its methods and
// stayed in package main. Only the tenant family moved here.
type Admin struct{ cp CP }

func NewAdmin(cp CP) Admin { return Admin{cp} }

// ListTenants (GET /api/admin/tenants) — overview for the admin UI.
// super_admin sees every tenant; a tenant_admin sees only the tenants they
// administer. The super_admin flag lets the Console hide deployment-wide controls
// (create tenant, tenant quotas, clean-home, role grants) for tenant_admins.
//
// The reserved tenant (system_tenant.go) is filtered out here, in the API layer, and
// deliberately not in `store.ListTenants`: that store call also backs the audit view's
// tenant_id → slug resolution (audit.go) and the cost poller's membership → tenant
// resolution (cloudcost.go), both of which would start producing rows with no tenant if it
// disappeared underneath them. The Console also feeds this list to the tenant filter of
// every cross-cutting view (sessions / uptime / cost / audit / MCP), so filtering in this
// one place removes it everywhere.
func (a Admin) ListTenants(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	isSuper := ident.Role == "super_admin"

	var tenants []store.Tenant
	if isSuper {
		ts, err := a.cp.Store().ListTenants(r.Context())
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, t := range ts {
			if a.cp.IsSystemTenantSlug(t.Slug) {
				continue
			}
			tenants = append(tenants, t)
		}
	} else {
		ms, err := a.cp.Store().ListMemberships(r.Context(), ident.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, m := range ms {
			if m.Role != "tenant_admin" {
				continue
			}
			if t, err := a.cp.Store().GetTenant(r.Context(), m.TenantID); err == nil {
				tenants = append(tenants, t)
			}
		}
	}

	out := make([]map[string]any, 0, len(tenants))
	for _, t := range tenants {
		members, _ := a.cp.Store().ListMembersByTenant(r.Context(), t.ID)
		running, _ := a.cp.CountRunningInTenant(r.Context(), t.ID)
		lim := a.cp.ParseLimits(t.Limits)
		row := map[string]any{
			"slug": t.Slug, "name": t.Name, "status": t.Status, "isolation": t.Isolation,
			"users": len(members), "running": running,
			"max_workspaces": lim.MaxWorkspaces, "max_sessions": lim.MaxSessions,
			"max_git_repos":         lim.MaxGitRepos,
			"max_lfs_bytes":         lim.MaxLFSBytes,
			"max_workspace_mem":     lim.MaxWorkspaceMem,
			"max_workspace_cpu":     lim.MaxWorkspaceCPU,
			"max_workspace_disk_gb": lim.MaxWorkspaceDiskGB,
			"allowed_slot_classes":  lim.AllowedSlotClasses,
			"slot_class":            lim.SlotClass,
			"session_idle_timeout":  lim.SessionIdleTimeout, "ws_idle_timeout": lim.WSIdleTimeout,
			"interaction_idle_timeout":        lim.InteractionIdleTimeout,
			"home_hibernate_after":            lim.HomeHibernateAfter,
			"home_backup_every":               lim.HomeBackupEvery,
			"allow_agent_self_update":         lim.AllowAgentSelfUpdate,
			"allow_engine_ingest":             lim.AllowEngineIngest,
			"terminal_history_retention_days": lim.TerminalHistoryRetentionDays,
			// Per-tenant login rules (docs/log/61 §61.9.7), for the admin editor.
			"allowed_providers": t.AllowedProviders,
			"auto_join_domains": t.AutoJoinDomains,
			"allowed_domains":   t.AllowedDomains,
			"hidden_providers":  t.HiddenProviders,
		}
		// allow_engine_llm / allow_engine_image (ADR 0084 decision 7): super_admin only. This
		// is not "who may run what" (a tenant_admin's business) but "may this tenant spend on
		// a GPU box at all" — a cost decision the operator alone makes, so a tenant_admin's own
		// row never carries it. Resolved to the effective bool (nil => true) for display; a
		// save always writes an explicit true/false regardless of what was read here.
		if isSuper {
			row["allow_engine_llm"] = lim.AllowEngineLLM == nil || *lim.AllowEngineLLM
			row["allow_engine_image"] = lim.AllowEngineImage == nil || *lim.AllowEngineImage
		}
		out = append(out, row)
	}
	resp := map[string]any{"tenants": out, "super_admin": isSuper}
	// Only the deployment operator can act on these (they are CP environment), so a
	// tenant_admin's answer does not carry them.
	if isSuper {
		resp["deployment_warnings"] = a.cp.DeploymentWarnings()
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetTenantLogin (PUT /api/admin/tenants/{slug}/login) stores the per-tenant login
// rules (docs/log/61 §61.9.7). super_admin only, deliberately: two of the three fields
// reach past the tenant. auto_join_domains widens the DEPLOYMENT's entry gate — a
// whole email domain gets in — and allowed_providers decides which IdP is trusted
// to say who someone is. Those are the operator's calls (decisions 24/25), while what a
// tenant_admin owns is the roster inside their own tenant.
func (a Admin) SetTenantLogin(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var body struct {
		AllowedProviders string `json:"allowed_providers"`
		AutoJoinDomains  string `json:"auto_join_domains"`
		AllowedDomains   string `json:"allowed_domains"`
		HiddenProviders  string `json:"hidden_providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	t, ok, err := a.cp.Store().GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	provs := a.cp.SplitCSVLower(body.AllowedProviders)
	autoJoin := a.cp.SplitDomainCSV(body.AutoJoinDomains)
	allowed := a.cp.SplitDomainCSV(body.AllowedDomains)
	hidden := a.cp.SplitCSVLower(body.HiddenProviders)

	// Naming a provider the deployment does not have would silently produce a login
	// page with no buttons, so refuse it here rather than at 3am.
	//
	// A "t:<slug>:<name>" id is one of the tenant's own sign-in methods (docs/log/61
	// §61.11), which is how a subsidiary says "only our Entra, please". It is checked
	// against that tenant's rows instead of knownProviderIDs (which holds env ids
	// only), and the slug must be this tenant's: naming another tenant's method would
	// produce a button nobody here can use, since the tenant gate pins such a session
	// to its own tenant anyway (decision 32-3).
	var ownIdP map[string]bool
	known := a.cp.KnownProviderIDs()
	// hidden_providers is validated by the same rule, and for the same reason: a
	// typo there is silent (the button simply keeps appearing) and nothing else in
	// the system will ever mention it. It is checked TOGETHER with allowed_providers
	// so both halves of "which methods does this tenant know about" stay one rule
	// (docs/log/61 §61.15.9).
	for _, p := range append(append([]string(nil), provs...), hidden...) {
		if slug, name, isTenant := auth.ParseTenantProviderID(p); isTenant {
			if ownIdP == nil {
				rows, err := a.cp.Store().ListTenantIdPs(r.Context(), t.ID)
				if err != nil {
					writeAPIErr(w, internalErr(err))
					return
				}
				ownIdP = map[string]bool{}
				for _, row := range rows {
					ownIdP[row.Name] = true
				}
			}
			if slug != t.Slug || !ownIdP[name] {
				writeAPIErr(w, &APIError{http.StatusBadRequest, "unknown_provider",
					"tenant " + t.Slug + " has no sign-in method named " + p})
				return
			}
			continue
		}
		if known != nil && !known[p] {
			writeAPIErr(w, &APIError{http.StatusBadRequest, "unknown_provider",
				"no login provider named " + p + " is enabled on this deployment"})
			return
		}
	}
	// ★ One domain, one tenant (docs/log/61 §61.9.8). The resolution rule ("lowest slug
	// wins") exists for rows that predate this check; the check exists so nobody
	// has to rely on it. Rejecting on save is the only place a human is present to
	// read the reason.
	rules, err := a.cp.Store().ListTenantLoginRules(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, other := range rules {
		if other.ID == t.ID {
			continue
		}
		for _, d := range autoJoin {
			for _, od := range other.AutoJoinDomains {
				if d == od {
					writeAPIErr(w, &APIError{http.StatusConflict, "auto_join_conflict",
						"domain " + d + " is already an auto-join domain of tenant " + other.Slug})
					return
				}
			}
		}
	}

	if err := a.cp.Store().SetTenantLogin(r.Context(), t.ID, a.cp.JoinCSV(provs), a.cp.JoinCSV(autoJoin), a.cp.JoinCSV(allowed), a.cp.JoinCSV(hidden)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// These rules ARE the entry gate; a cached copy would keep letting people in.
	a.cp.InvalidateTenantLogin()
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.login_rules", Target: t.Slug,
		Detail: "providers=" + a.cp.JoinCSV(provs) + " auto_join=" + a.cp.JoinCSV(autoJoin) +
			" allowed=" + a.cp.JoinCSV(allowed) + " hidden=" + a.cp.JoinCSV(hidden),
		At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, tenantLoginWire{
		Tenant: t.Slug, AllowedProviders: a.cp.JoinCSV(provs),
		AutoJoinDomains: a.cp.JoinCSV(autoJoin), AllowedDomains: a.cp.JoinCSV(allowed),
		HiddenProviders: a.cp.JoinCSV(hidden),
	})
}

// ListMembers (GET /api/admin/tenants/{slug}/members).
func (a Admin) ListMembers(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	members, err := a.cp.Store().ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Removed members stay on the ADMIN roster (and only here) so the rest of the
	// offboarding sequence — stop the workspace, wipe the home — is still
	// reachable after access has been revoked (docs/log/61 §61.10.6).
	removed, err := a.cp.Store().ListRemovedMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(members)+len(removed))
	add := func(list []store.MemberInfo, status string) {
		for _, m := range list {
			container, state := a.cp.WorkspaceStateByMembership(r.Context(), m.MembershipID)
			row := map[string]any{
				"user_key": m.UserKey, "email": m.Email, "role": m.MemberRole,
				"super_admin": m.IdentityRole == "super_admin",
				"container":   container, "state": state,
				"status": status,
			}
			// Why the CP itself stopped it (#1384): the member was notified, but a notification
			// never reaches the admin, who would otherwise see plain "stopped".
			if as := store.CurrentAutoStop(r.Context(), a.cp.Store(), m.MembershipID, state); as != nil {
				row["auto_stop"] = as
			}
			// The idle forecast (docs/log/75 P4): the reaper's last observation of
			// when this stops and who is holding it open. The point is that nothing
			// is recomputed here — a screen that derived it itself would drift from
			// what the reaper actually looks at (presence, pins, background work),
			// and the screen you go to in order to investigate would give a
			// different answer. Only meaningful for a running workspace.
			if wsRow, ok, _ := a.cp.Store().GetWorkspaceByMembership(r.Context(), m.MembershipID); ok {
				if f, has := a.cp.IdleForecastFor(wsRow.ID); has && state == "running" {
					row["idle"] = f
				}
			}
			if ul, ok, _ := a.cp.Store().GetUserLimit(r.Context(), m.MembershipID); ok {
				row["max_sessions"] = ul.MaxSessions
				row["mem_limit"] = ul.MemLimit
				row["cpu_limit"] = ul.CPULimit
				row["disk_gb"] = ul.DiskGB
				row["slot_class"] = ul.SlotClass
			}
			out = append(out, row)
		}
	}
	add(members, "active")
	add(removed, "removed")
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "members": out})
}

// StopWorkspace (POST /api/admin/stop-workspace {tenant_slug,user_key}).
func (a Admin) StopWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	if body.UserKey == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "user_key required"})
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := a.cp.StopWorkspaceByMembership(r.Context(), mem.ID); err != nil {
		if errors.Is(err, store.ErrSessionShareOwnerBusy) {
			writeAPIErr(w, a.cp.WorkspaceLifecycleLeaseError(err))
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": body.UserKey, "tenant": t.Slug})
}

// CleanHome (POST /api/admin/clean-home {tenant_slug,user_key}) wipes a
// member's workspace home except auth/connection state. Same target resolution as
// stop-workspace; the container is stopped first.
//
// tenant_admin, not super_admin (docs/log/61 §61.10.6 + decision 26). The offboarding
// sequence is "deactivate the membership → stop the workspace → wipe the home", and the
// department is who knows that somebody left; leaving only this last step with the operator
// turned every leaver into a ticket for IT. The gate is tenantAdminFor, exactly as
// stopWorkspace already does it, so a tenant_admin only ever reaches their own members'
// homes.
//
// This widens a permission, so it is always audited: who wiped whose home in which tenant.
// The request is recorded before anything is stopped, and refused when that record cannot
// be written (beginIrreversible). The wipe and its outcome entry do not follow the request:
// the wipe can outlast the ingress idle timeout on ecs-ec2.
//
// A runtime that cannot reach the home is refused with home_wipe_unsupported before
// anything is stopped; its outcome entry says so.
//
// Where the erase is a Fargate task (ecs, HomeOpsInBackground) it takes minutes: the
// request is answered 202 {pending: true} once the workspace is stopped, and the outcome
// entry is written when the task has finished.
func (a Admin) CleanHome(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	if body.UserKey == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "user_key required"})
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "workspace.clean_home", Target: ident.UserKey,
	})
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if a.cp.HomeOpsInBackground() {
		err := a.cp.StartCleanHomeByMembership(ctx, mem.ID, store.HomeOpAudit{Base: in.Outcome(),
			OK: "home erased", OKStatus: http.StatusOK,
			FailPrefix: "error: ", FailStatus: http.StatusInternalServerError})
		if err != nil {
			refuseIrreversible(w, r, in, a.homeOpRefusal(err, "clean home"))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"cleaned": body.UserKey, "tenant": t.Slug, "pending": true})
		return
	}
	if err := a.cp.CleanHomeByMembership(ctx, mem.ID); err != nil {
		refuseIrreversible(w, r, in, a.homeOpRefusal(err, "clean home"))
		return
	}
	in.Done(ctx, "home erased", http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{"cleaned": body.UserKey, "tenant": t.Slug})
}

// homeOpRefusal maps what refused a Clean home or a Destroy to its answer.
func (a Admin) homeOpRefusal(err error, op string) *APIError {
	switch {
	case errors.Is(err, store.ErrSessionShareOwnerBusy):
		return a.cp.WorkspaceLifecycleLeaseError(err)
	case errors.Is(err, runtime.ErrHomeWipeUnsupported):
		return &APIError{http.StatusNotImplemented, "home_wipe_unsupported",
			op + " is not available on this deployment: its runtime cannot reach the workspace home"}
	case errors.Is(err, runtime.ErrHomeTaskInFlight):
		return &APIError{http.StatusConflict, "home_operation_in_progress", err.Error()}
	}
	return internalErr(err)
}

// HomeBackups (GET /api/admin/tenants/{slug}/members/{key}/home-backups) counts the copies
// the runtime keeps of a member's home outside it (ecs-ec2's backup snapshots). Clean home
// leaves them on purpose, so an administrator finishing an offboarding has to be able to
// see that they exist before deciding to delete them. tenant_admin, like clean-home.
func (a Admin) HomeBackups(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	mem, _, _, aerr := a.cp.ResolveMember(r, t.Slug, r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	b, supported, err := a.cp.HomeBackupsByMembership(r.Context(), mem.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !supported {
		writeAPIErr(w, homeBackupsUnsupported())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// DeleteHomeBackups (DELETE /api/admin/tenants/{slug}/members/{key}/home-backups) deletes
// those copies: the deliberate step Clean home leaves out, for a home that must not
// survive anywhere. tenant_admin, the same gate as clean-home, and audited with how many
// copies went. As in CleanHome, the request is recorded first, the deletion and its outcome
// entry do not follow the request, and copies deleted before a failure are still counted. While the home itself
// still exists, the tenant's backup schedule goes on taking copies of it; an offboarding
// cleans the home first.
func (a Admin) DeleteHomeBackups(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	caller, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	mem, _, _, aerr := a.cp.ResolveMember(r, t.Slug, key)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "workspace.delete_backups", Target: key,
	})
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	n, supported, err := a.cp.DeleteHomeBackupsByMembership(ctx, mem.ID)
	if !supported && err == nil {
		refuseIrreversible(w, r, in, homeBackupsUnsupported())
		return
	}
	detail := fmt.Sprintf("backup copies of the home deleted: %d", n)
	if err != nil {
		detail += "; the rest failed: " + err.Error()
		in.Done(ctx, detail, http.StatusInternalServerError)
		writeAPIErr(w, internalErr(err))
		return
	}
	in.Done(ctx, detail, http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "tenant": t.Slug})
}

func homeBackupsUnsupported() *APIError {
	return &APIError{http.StatusNotImplemented, "home_backups_unsupported",
		"this deployment keeps no backup copies of workspace homes"}
}

// DestroyWorkspace (DELETE /api/admin/workspaces {tenant_slug,user_key}) is the
// irreversible one: it deletes the home and every per-membership resource the runtime
// created, then the DB row. ADR 0045 decision 13-2.
//
// It is a separate operation from removing the membership on purpose. Offboarding is a
// logical delete that keeps the home so a returning member is just a re-invite
// (docs/log/61 §61.10.6); destroying is the second, deliberate step you take later — when the
// EBS volume behind a long-gone member is still being billed for. Doing both at once is
// possible (removeMembership's purge flag) but never the default.
//
// Only an inactive membership can be destroyed. In the admin UI this operation sits one
// misclick away from a member who is at their desk, and there is no undo.
//
// It overrides the deletion locks of ADR 0028 and cannot do otherwise: the locks live
// inside the home, which is unreadable while the workspace is stopped
// (docs/log/64 §64.18.1). The Console has to say so.
//
// tenant_admin (their own tenant) or super_admin — the same gate as clean-home, which is
// already "destroy this person's work" in every sense except the billing.
//
// On ecs and ecs-ec2 with the home task (DestroyInBackground) removing the EFS directories
// takes minutes: answered 202 {pending: true}, the outcome entry written when it has
// finished.
func (a Admin) DestroyWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if mem.Status == "active" {
		writeAPIErr(w, &APIError{http.StatusConflict, "membership_active",
			"remove the membership first; an active member's workspace cannot be destroyed"})
		return
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "workspace.destroy", Target: ident.UserKey,
	})
	if !ok {
		return
	}
	if a.cp.DestroyInBackground() {
		ctx := context.WithoutCancel(r.Context())
		err := a.cp.StartDestroyWorkspaceByMembership(ctx, mem.ID, store.HomeOpAudit{Base: in.Outcome(),
			OK: "workspace destroyed (home and runtime resources deleted)", Leftovers: true, OKStatus: http.StatusOK,
			FailPrefix: "error: ", FailStatus: http.StatusInternalServerError})
		if err != nil {
			refuseIrreversible(w, r, in, a.homeOpRefusal(err, "destroy"))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"destroyed": ident.UserKey, "tenant": t.Slug, "pending": true})
		return
	}
	leftovers, err := a.cp.DestroyWorkspaceByMembership(r.Context(), mem.ID)
	if err != nil {
		refuseIrreversible(w, r, in, a.homeOpRefusal(err, "destroy"))
		return
	}
	in.Done(r.Context(), destroyedDetail("workspace destroyed (home and runtime resources deleted)", leftovers), http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{
		"destroyed": ident.UserKey, "tenant": t.Slug, "leftovers": leftovers,
	})
}

// destroyedDetail is store.DestroyedDetail: a Destroy finished by the CP's reconciler
// (#1544) has to write the same entry.
func destroyedDetail(detail string, leftovers []string) string {
	return store.DestroyedDetail(detail, leftovers)
}

// CreateTenant (POST /api/admin/tenants {slug,name}).
func (a Admin) CreateTenant(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	var body struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	slug := a.cp.SanitizeUser(body.Slug)
	if slug == "" || slug == auth.DefaultTenantSlug {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid slug"})
		return
	}
	if t, ok, err := a.cp.Store().GetTenantBySlug(r.Context(), slug); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if ok {
		writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": t.Name, "existed": true})
		return
	}
	name := body.Name
	if name == "" {
		name = slug
	}
	t, err := a.cp.Store().CreateTenant(r.Context(), slug, name)
	if err != nil {
		writeAPIErr(w, dataRootNameErr(err, http.StatusBadRequest, "tenant_slug_reserved", "tenant_slug_conflict"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": t.Name})
}

// userKeyErr refuses a client-supplied user_key that a new identity must not get. The key
// is the home directory name (workspaceNames joins it under the data root), and only the
// docker runtime refuses `/`, `..` or upper case on its own; the native runtime would let
// such a key leave <WS_DATA>/<slug>/. Accepted are the two forms the server mints itself:
// sanitizeUser's fixed points, and disambiguateUserKey's "<fixed point>-<8 hex>", which
// can run past sanitizeUser's 40 characters. Only AddMembership needs it: the other
// member handlers look the key up and never mint one.
func (a Admin) userKeyErr(key string) *APIError {
	if key == a.cp.SanitizeUser(key) {
		return nil
	}
	if prefix, ok := store.SplitDisambiguatedUserKey(key); ok && prefix == a.cp.SanitizeUser(prefix) {
		return nil
	}
	msg := "invalid user_key: use lowercase letters and digits, separated by single hyphens, at most 40 characters"
	if s := a.cp.SanitizeUser(key); s != "" {
		msg += " (for example " + strconv.Quote(s) + ")"
	}
	return &APIError{http.StatusBadRequest, "bad_request", msg}
}

// memberIdentity returns key's identity when it already holds a membership of t. Only
// that re-invite may use a key outside userKeyErr's forms: it reuses the stored identity
// and membership, so no new home is made from the key. Any other tenant is refused. A
// lookup error answers false.
func (a Admin) memberIdentity(r *http.Request, t store.Tenant, key string) (store.Identity, bool) {
	ident, ok, err := a.cp.Store().GetIdentityByUserKey(r.Context(), key)
	if err != nil || !ok {
		return store.Identity{}, false
	}
	if _, ok, err = a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID); err != nil || !ok {
		return store.Identity{}, false
	}
	return ident, true
}

// dataRootNameErr maps the store's data-root name refusals (store_dataroot.go) to API
// errors; a name another party already holds is always a conflict. Anything else stays
// internal.
func dataRootNameErr(err error, reservedStatus int, reservedCode, takenCode string) *APIError {
	switch {
	case errors.Is(err, store.ErrDataRootNameReserved):
		return &APIError{reservedStatus, reservedCode, err.Error()}
	case errors.Is(err, store.ErrDataRootNameTaken):
		return &APIError{http.StatusConflict, takenCode, err.Error()}
	}
	return internalErr(err)
}

// AddMembership (POST /api/admin/memberships {email|user_key, tenant_slug, role}).
// Pre-creates the target identity if needed (invite-by-key/email).
func (a Admin) AddMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email      string `json:"email"`
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	key := body.UserKey
	// reinvite is set when key is outside the minted forms but already a member of t; the
	// stored identity is then used as is, never passed through UpsertIdentity.
	var reinvite *store.Identity
	if key != "" {
		if aerr := a.userKeyErr(key); aerr != nil {
			ident, ok := a.memberIdentity(r, t, key)
			if !ok {
				writeAPIErr(w, aerr)
				return
			}
			// A different address would send UpsertIdentity's disambiguation to mint
			// "<key>-<hash>", a new identity on the very key this branch exempts.
			if body.Email != "" && !strings.EqualFold(strings.TrimSpace(body.Email), ident.Email) {
				writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request",
					"this user_key can only be re-invited without an email or with its stored one"})
				return
			}
			reinvite = &ident
		}
	} else {
		key = a.cp.SanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	// Only a super_admin may mint a tenant_admin (privilege escalation); a
	// tenant_admin adding members can only add plain members.
	role := "member"
	if body.Role == "tenant_admin" && caller.Role == "super_admin" {
		role = "tenant_admin"
	}
	// ★ allowed_domains is a guard on THIS call and nowhere else (docs/log/61 §61.9.5).
	// It stops a tenant_admin from putting an address outside their department's
	// domain on the roster. It is deliberately not re-checked per request: doing so
	// would lock out the contractor somebody invited on purpose, which would then
	// need an exception list — a second roster, which is what this design avoids.
	if aerr := a.checkInviteDomain(r, t, body.Email, key); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var ident store.Identity
	if reinvite != nil {
		ident = *reinvite
	} else {
		var err error
		if ident, err = a.cp.Store().UpsertIdentity(r.Context(), body.Email, key, ""); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	mem, err := a.cp.Store().EnsureMembership(r.Context(), ident.ID, t.ID, role)
	if err != nil {
		writeAPIErr(w, dataRootNameErr(err, http.StatusConflict, "user_key_reserved", "user_key_conflict"))
		return
	}
	// ★ Re-inviting somebody who was removed puts them back. EnsureMembership
	// deliberately does not reactivate (it also serves the auto-provisioning paths,
	// where that would undo an offboarding on the person's next visit) — so an
	// invite, which IS an explicit decision, does it here.
	detail := "role=" + role
	if mem.Status != "active" {
		if err := a.cp.Store().SetMembershipStatus(r.Context(), mem.ID, "active"); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		// The scheduler paused this person's schedules while they were away rather than
		// deleting them; a restore is meant to give back what the removal took, so they
		// resume here. A failure is recorded, not answered: the person IS back.
		n, err := a.cp.ResumeSchedulesHeldByRemoval(r.Context(), mem.ID)
		detail += "; restored; schedules resumed=" + strconv.Itoa(n)
		if err != nil {
			detail += " (resume failed: " + err.Error() + ")"
		}
	}
	// Being on a roster is an entry-gate term now (docs/log/61 §61.9.6) — an invited
	// person must be able to sign in immediately, not after the cache expires.
	a.cp.InvalidateTenantLogin()
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "membership.add", Target: ident.UserKey, Detail: detail, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user_key": ident.UserKey, "tenant": t.Slug, "role": role})
}

// checkInviteDomain applies tenant.allowed_domains to an invite. The address is
// the one being invited, or — when the invite names only a user_key — the one
// already on that identity. An invite with no address at all cannot be checked, so
// a tenant that set a guard refuses it rather than letting it through unexamined.
func (a Admin) checkInviteDomain(r *http.Request, t store.Tenant, email, key string) *APIError {
	domains := a.cp.SplitDomainCSV(t.AllowedDomains)
	if len(domains) == 0 {
		return nil
	}
	if email == "" {
		if ident, ok, err := a.cp.Store().GetIdentityByUserKey(r.Context(), key); err == nil && ok {
			email = ident.Email
		}
	}
	if email == "" {
		return &APIError{http.StatusBadRequest, "email_required",
			"tenant " + t.Slug + " restricts invites to " + a.cp.JoinCSV(domains) + "; invite by email address"}
	}
	if !a.cp.DomainMatches(domains, email) {
		return &APIError{http.StatusForbidden, "domain_not_allowed",
			"tenant " + t.Slug + " only accepts members from: " + a.cp.JoinCSV(domains)}
	}
	return nil
}

// RemoveMembership (DELETE /api/admin/memberships {tenant_slug,user_key}) takes
// somebody off a tenant's roster — the transfer/leaver operation docs/log/61 §61.10.6
// found missing, and the one that P3 makes load-bearing: once the roster is also
// the entry gate (§61.9.6), being unable to remove a row means being unable to
// offboard at all. A signed session cookie is valid for up to AF_SESSION_TTL
// (7 days by default) and cannot be revoked individually, so without this the
// person keeps their access for a week after they leave (decisions 22/27).
//
// The delete is LOGICAL (status='inactive'): the workspace row, its home and its
// encrypted secrets survive, and every resolution path already requires an active
// membership, so access stops on the very next request. The running workspace is
// stopped (stopRemovedWorkspace) and the scheduler pauses the person's schedules.
// Deleting the row outright would orphan the schedules, audit entries and shares that
// reference it. Reinstating is re-inviting (AddMembership), which reactivates the row
// and resumes those schedules.
//
// The request is recorded before the status changes (beginIrreversible), purge or not: the
// deactivation is the offboarding itself, and purge makes it irreversible.
//
// tenant_admin (their own tenant) or super_admin, matching who owns the roster.
func (a Admin) RemoveMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		// Purge additionally destroys the workspace — home, and every per-membership
		// resource the runtime created (ADR 0045 decision 13-2). Default false: the logical
		// delete above is what offboarding means, and this is a second, irreversible
		// thing that happens to be convenient to do in the same click. It runs AFTER
		// the membership is deactivated, so a failure here still leaves the person
		// locked out.
		Purge bool `json:"purge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	// ★ Your own membership: allowed, but never the LAST one. What has no undo from
	// inside the product is losing your own way back in, and that is a property of
	// "the last one" rather than of "one of mine" — refusing every self-removal made
	// a throwaway tenant impossible to clean up, because the operator who created it
	// is the only member it has (docs/log/64 §64.28: the golden bake's seed has to be
	// somebody who can sign in, so it IS your own account, and a single-admin
	// deployment has no other administrator to ask). The count is of ACTIVE
	// memberships other than this one, so a row that is already inactive is not
	// counted as the way back in.
	if ident.ID == caller.ID {
		mine, err := a.cp.Store().ListMemberships(r.Context(), ident.ID) // active only
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		others := 0
		for _, v := range mine {
			if v.MembershipID != mem.ID {
				others++
			}
		}
		if others == 0 {
			writeAPIErr(w, &APIError{http.StatusBadRequest, "self_removal",
				"you cannot remove your own last membership; ask another administrator"})
			return
		}
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "membership.remove", Target: ident.UserKey,
		Detail: "purge=" + strconv.FormatBool(body.Purge),
	})
	if !ok {
		return
	}
	if err := a.cp.Store().SetMembershipStatus(r.Context(), mem.ID, "inactive"); err != nil {
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	// Drop the cached runtime as well as the login caches: the workspace stays on
	// disk, but nothing should keep serving it from memory for this membership.
	a.cp.EvictMembershipCache(mem.ID)
	a.cp.InvalidateTenantLogin()
	// Requests already authorised for this membership — a terminal, an event stream, a
	// shared preview's WebSocket — would otherwise outlive the removal; every other replica
	// closes its own on the next removed-member sweep.
	closed := a.cp.CloseMembershipConnections(mem.ID)
	var detail, workspaceStop string
	var leftovers []string
	if !body.Purge {
		workspaceStop = a.stopRemovedWorkspace(r.Context(), t.ID, caller.ID, ident.UserKey, mem.ID)
		detail = "status=inactive (home kept); connections closed=" + strconv.Itoa(closed) +
			"; workspace stop " + workspaceStop
	} else if a.cp.DestroyInBackground() {
		// The membership is inactive now; destroying its workspace takes minutes here, so
		// the outcome entry waits for it.
		ctx := context.WithoutCancel(r.Context())
		err := a.cp.StartDestroyWorkspaceByMembership(ctx, mem.ID, store.HomeOpAudit{Base: in.Outcome(),
			OK: "status=inactive; workspace destroyed (purge)", Leftovers: true, OKStatus: http.StatusOK,
			FailPrefix: "status=inactive; purge FAILED: ", FailStatus: http.StatusInternalServerError})
		if err != nil {
			in.Done(r.Context(), "status=inactive; purge FAILED: "+err.Error(), http.StatusInternalServerError)
			writeAPIErr(w, &APIError{http.StatusInternalServerError, "purge_failed",
				"the membership was deactivated but the workspace could not be destroyed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"removed": ident.UserKey, "tenant": t.Slug, "purged": true, "pending": true,
		})
		return
	} else {
		leftovers, err = a.cp.DestroyWorkspaceByMembership(r.Context(), mem.ID)
		if err != nil {
			// The membership IS deactivated at this point — say so rather than
			// returning a bare 500 that reads as "nothing happened".
			in.Done(r.Context(), "status=inactive; purge FAILED: "+err.Error(), http.StatusInternalServerError)
			writeAPIErr(w, &APIError{http.StatusInternalServerError, "purge_failed",
				"the membership was deactivated but the workspace could not be destroyed: " + err.Error()})
			return
		}
		detail = destroyedDetail("status=inactive; workspace destroyed (purge)", leftovers)
	}
	in.Done(r.Context(), detail, http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{
		"removed": ident.UserKey, "tenant": t.Slug,
		"purged": body.Purge, "leftovers": leftovers, "workspace_stop": workspaceStop,
	})
}

// RemovalStopWait is how long RemoveMembership waits for the removed member's workspace to
// stop before answering. A stop that takes longer carries on in the background and writes
// its own audit row (membership.remove.stop_workspace): an ECS drain can take minutes, and
// the administrator's request must not hang on it. A variable so tests can shorten it.
var RemovalStopWait = 10 * time.Second

// errRestoredBeforeStop marks a removal stop that found the person re-invited by the time
// it held the workspace's locks, and so stopped nothing.
var errRestoredBeforeStop = errors.New("membership restored before the stop")

// removalStopBudget bounds the background stop, retries included.
const removalStopBudget = 10 * time.Minute

// removalStopRetry is the pause between attempts while another lifecycle operation holds
// the workspace (a start in flight when the member was removed).
var removalStopRetry = 2 * time.Second

// stopRemovedWorkspace stops the workspace of a membership that was just deactivated and
// says how that went: "none" (no workspace), "stopped", "skipped: membership restored",
// "failed: …" or "pending". Whatever happens here, the removed-member sweep stops a
// workspace this leaves running (a CP restart mid-stop, a stop past its budget).
//
// Why removal stops it at all: every CP route a workspace can call already refuses an
// inactive membership on the next request, but the container itself goes on running. It
// still holds what it pulled while the person was a member (the tenant's distributed MCP
// headers, git and cloud logins in the home), its sessions keep spending the tenant's
// compute, and nobody can see it — the Console, schedules and shares all resolve through
// an active membership. Only this membership's workspace is touched: the same person
// keeps their workspace in every other tenant.
//
// The stop is the administrator's ordinary stop (StopWorkspaceByMembership), which works
// on an inactive membership because it looks the row up by id.
func (a Admin) stopRemovedWorkspace(ctx context.Context, tenantID, actorID, target, membershipID string) string {
	ctx = context.WithoutCancel(ctx)
	if _, ok, err := a.cp.Store().GetWorkspaceByMembership(ctx, membershipID); err == nil && !ok {
		return "none"
	}
	done := make(chan error, 1)
	go func() {
		bctx, cancel := context.WithTimeout(ctx, removalStopBudget)
		defer cancel()
		for {
			stopped, err := a.cp.StopRemovedMemberWorkspace(bctx, membershipID)
			if err == nil && !stopped {
				err = errRestoredBeforeStop
			}
			if !errors.Is(err, store.ErrSessionShareOwnerBusy) {
				done <- err
				return
			}
			select {
			case <-bctx.Done():
				done <- err
				return
			case <-time.After(removalStopRetry):
			}
		}
	}()
	outcome := func(err error) string {
		switch {
		case errors.Is(err, errRestoredBeforeStop):
			return "skipped: membership restored"
		case err != nil:
			return "failed: " + err.Error()
		}
		return "stopped"
	}
	timer := time.NewTimer(RemovalStopWait)
	defer timer.Stop()
	select {
	case err := <-done:
		return outcome(err)
	case <-timer.C:
	}
	go func() {
		err := <-done
		status := http.StatusOK
		if err != nil && !errors.Is(err, errRestoredBeforeStop) {
			status = http.StatusInternalServerError
		}
		if aerr := a.cp.Store().InsertAudit(ctx, store.AuditLog{
			ID: store.NewID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
			Action: "membership.remove.stop_workspace", Target: target,
			Detail: "workspace stop " + outcome(err), HTTPStatus: status, At: store.NowTS(),
		}); aerr != nil {
			log.Printf("membership.remove: workspace stop for %s finished (%s) but its audit row was not written: %v",
				target, outcome(err), aerr)
		}
	}()
	return "pending"
}

// DeleteMembership (DELETE /api/admin/tenants/{slug}/members/{key}) removes the row
// itself — the third and last step of the clean-up sequence (docs/log/61 §61.18):
//
//	remove the member → destroy the workspace → delete the member for good
//
// What a hard delete must not take with it is the HISTORY, and that is what is kept:
// audit_log (which never referenced a membership — its actor is an identity),
// cloud_cost_daily and usage_daily. Schedules and shares are this person's and go with
// them. An offboarding able to erase its own audit trail, or to change last month's
// invoice total, would be worse than one that leaves a dead row behind.
//
// The two refusals are the same line ADR 0045 decision 13-2 draws, for the same reason. An
// active member is somebody at their desk. A membership whose workspace row is still there
// owns a home, an EBS volume and EFS access points; deleting the row would leave those
// billing with nothing in the database pointing at them — the exact leak DestroyWorkspace
// exists to close.
//
// A reserved membership is refused outright (system_tenant.go): the golden baker recreates
// the seed and the probe on its next tick, so deleting them mid-bake only strands the slot
// they hold.
//
// tenant_admin (their own tenant) or super_admin — the same gate as destroyWorkspace,
// which is what has to have happened first.
func (a Admin) DeleteMembership(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	caller, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if a.cp.IsSystemTenantSlug(t.Slug) {
		writeAPIErr(w, &APIError{http.StatusConflict, "system_membership",
			"this membership belongs to the deployment, not to a person; the golden bake recreates it"})
		return
	}
	mem, _, hasWS, aerr := a.cp.ResolveMember(r, t.Slug, key)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if mem.Status == "active" {
		writeAPIErr(w, &APIError{http.StatusConflict, "membership_active",
			"remove the membership first; an active member's row cannot be deleted"})
		return
	}
	if hasWS {
		writeAPIErr(w, &APIError{http.StatusConflict, "workspace_present",
			"destroy the workspace first; deleting the row would leave the home and its cloud resources billing"})
		return
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "membership.delete", Target: key,
	})
	if !ok {
		return
	}
	if err := a.cp.Store().DeleteMembership(r.Context(), mem.ID); err != nil {
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	a.cp.EvictMembershipCache(mem.ID)
	a.cp.InvalidateTenantLogin()
	in.Done(r.Context(), "membership row and its per-membership rows deleted; audit, cost and occupancy history kept",
		http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": key, "tenant": t.Slug})
}

// DeleteTenant (DELETE /api/admin/tenants/{slug}) removes an EMPTY tenant. super_admin
// only, and it is the operation the product was missing entirely: tenants could be
// created and never removed, so a throwaway one kept its slot — on the production deployment two
// left over from the hand-baking era blocked the pool until the golden bake stopped too.
//
// ★ It only ever deletes what is already empty (docs/log/61 §61.18). Every refusal below is
// the same principle: the DB row is the only handle the deployment has on a cloud or
// disk resource, so it must never be the first thing to go.
//
//   - a system tenant — recreated by the next bake, so deleting it achieves nothing
//   - the default tenant — EnsureDefaultTenant recreates it at the next start
//   - an ACTIVE member — offboard them first; this is not an offboarding tool
//   - a workspace row — destroy it first, or its home/EBS/EFS keeps billing unreferenced
//   - an internal git repo — its bare and LFS objects live on disk
//
// ⚠️ The git repo refusal has an ORDERING trap, and the message has to say so:
// `DELETE /api/internal-git/repos/{name}` is gated by withMembership, so once the last
// member is off the roster NOBODY can delete those repos any more. They have to go while
// a member is still there. Deleting them from here instead was rejected: an operation
// whose name is "delete this tenant" must not silently destroy repositories.
func (a Admin) DeleteTenant(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	if a.cp.IsSystemTenantSlug(slug) {
		writeAPIErr(w, &APIError{http.StatusConflict, "system_tenant",
			"this tenant belongs to the deployment itself and is recreated automatically"})
		return
	}
	if slug == auth.DefaultTenantSlug {
		writeAPIErr(w, &APIError{http.StatusConflict, "default_tenant",
			"the default tenant is recreated at every start and cannot be deleted"})
		return
	}
	t, ok, err := a.cp.Store().GetTenantBySlug(ctx, slug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	active, err := a.cp.Store().ListMembersByTenant(ctx, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if len(active) > 0 {
		writeAPIErr(w, &APIError{http.StatusConflict, "tenant_not_empty",
			"remove this tenant's members first (" + strconv.Itoa(len(active)) + " left)"})
		return
	}
	wss, err := a.cp.Store().ListWorkspaces(ctx, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if len(wss) > 0 {
		writeAPIErr(w, &APIError{http.StatusConflict, "workspace_present",
			"destroy the workspaces of this tenant's removed members first (" + strconv.Itoa(len(wss)) + " left)"})
		return
	}
	repos, err := a.cp.Store().ListGitReposByTenant(ctx, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if len(repos) > 0 {
		writeAPIErr(w, &APIError{http.StatusConflict, "git_repos_present",
			"delete this tenant's " + strconv.Itoa(len(repos)) + " internal git repositories first — " +
				"they can only be deleted while a member is still on the roster"})
		return
	}
	removed, err := a.cp.Store().ListRemovedMembersByTenant(ctx, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// ⚠️ The slug goes in Target: the audit view resolves tenant_id → slug through
	// ListTenants (audit.go), so these rows' tenant column is blank once the tenant is gone.
	// The name has to be inside the entry itself.
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.delete", Target: t.Slug, Detail: "tenant \"" + t.Name + "\"",
	})
	if !ok {
		return
	}
	if err := a.cp.Store().DeleteTenant(ctx, t.ID); err != nil {
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	a.cp.EvictTenantCache(t.ID)
	a.cp.InvalidateTenantLogin()
	in.Done(ctx, "tenant \""+t.Name+"\" deleted; "+strconv.Itoa(len(removed))+
		" removed membership(s) deleted with it; audit, cost and occupancy history kept", http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": t.Slug, "memberships_deleted": len(removed),
	})
}

// SetTenantLimits (PUT /api/admin/tenants/{slug}/limits) — docs/16 P3-4.
func (a Admin) SetTenantLimits(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var body struct {
		MaxWorkspaces int   `json:"max_workspaces"`
		MaxSessions   int   `json:"max_sessions"`
		MaxGitRepos   int   `json:"max_git_repos"` // internal git repo cap (P2); 0 = unlimited
		MaxLFSBytes   int64 `json:"max_lfs_bytes"` // internal git LFS byte cap (P3); 0 = unlimited
		// Per-workspace RAM cap in BYTES (roadmap P3-4); 0 = no tenant cap. Bounds a
		// tenant_admin's per-user mem_limit at container start.
		MaxWorkspaceMem int64 `json:"max_workspace_mem"`
		// Per-workspace CPU (Fargate units) and working disk (GiB) ceilings; 0 = no cap.
		MaxWorkspaceCPU    int `json:"max_workspace_cpu"`
		MaxWorkspaceDiskGB int `json:"max_workspace_disk_gb"`
		// Which machine classes this tenant may choose from (docs/log/70 §70.4.3). Empty =
		// no restriction. The tenant's own DEFAULT is not here: that is a tenant_admin's
		// call and lives on PUT /api/admin/tenants/{slug}/slot-class.
		AllowedSlotClasses []string `json:"allowed_slot_classes"`
		// P3-9 idle-stop: duration strings ("30m"); "" => deployment default,
		// "0" => disabled for this tenant.
		SessionIdleTimeout string `json:"session_idle_timeout"`
		// Tier 1's second clock, for a session parked on a human decision — a
		// question, plan approval, a permission prompt, the rate-limit menu, expired
		// auth (docs/log/75 §75.5). "" => the tenant's session_idle_timeout, and the
		// deployment default if that is unset too.
		InteractionIdleTimeout string `json:"interaction_idle_timeout"`
		WSIdleTimeout          string `json:"ws_idle_timeout"`
		// Tier-3 home hibernation (ecs-ec2 only): "" => deployment default, "0" => never.
		HomeHibernateAfter string `json:"home_hibernate_after"`
		// Tier-4 home backup (ecs-ec2 only): the tenant's RPO. Same resolution.
		HomeBackupEvery string `json:"home_backup_every"`
		// Operator gate for member CLI self-update (claude/opencode/codex).
		AllowAgentSelfUpdate bool `json:"allow_agent_self_update"`
		// Operator gate for taking models into the engine catalogue (ADR 0072 open
		// question 11). The catalogue stays deployment-wide -- this only says whose
		// tenant_admins may add to it.
		AllowEngineIngest            bool `json:"allow_engine_ingest"`
		TerminalHistoryRetentionDays int  `json:"terminal_history_retention_days"`
		// Operator gate for USING the deployment's self-hosted engines (ADR 0084 decision
		// 7), as opposed to AllowEngineIngest above which only gates adding to the
		// catalogue. *bool, matching tenantLimits: nil resolves to allowed. This handler
		// rewrites the whole blob (⚠️ below), so a caller that omits the field does not
		// preserve a prior denial — it resets it to nil/allowed, same as any other field
		// here. The Console always sends an explicit true/false for exactly this reason.
		AllowEngineLLM   *bool `json:"allow_engine_llm"`
		AllowEngineImage *bool `json:"allow_engine_image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	if d := body.TerminalHistoryRetentionDays; d != 0 && d != 1 && d != 7 && d != 30 {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_retention", "terminal history retention must be 0, 1, 7, or 30 days"})
		return
	}
	// The int quotas were stored with NO validation at all. 0 means unlimited on every
	// one of them, so a negative is not a smaller quota — it is a quota nothing can
	// satisfy: max_workspaces=-1 makes `running >= limit` true before anyone starts, and
	// that tenant can never open a workspace again. Reject it here; a typo in a number
	// field is not something to discover from a member's failed Start.
	for _, q := range []struct {
		name string
		v    int64
	}{
		{"max_workspaces", int64(body.MaxWorkspaces)}, {"max_sessions", int64(body.MaxSessions)},
		{"max_git_repos", int64(body.MaxGitRepos)}, {"max_lfs_bytes", body.MaxLFSBytes},
		{"max_workspace_mem", body.MaxWorkspaceMem}, {"max_workspace_cpu", int64(body.MaxWorkspaceCPU)},
		{"max_workspace_disk_gb", int64(body.MaxWorkspaceDiskGB)},
	} {
		if q.v < 0 {
			writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_limit", q.name + " cannot be negative (0 = unlimited)"})
			return
		}
	}
	// Reject unparseable durations up front (empty stays empty = use default).
	for _, v := range []string{body.SessionIdleTimeout, body.InteractionIdleTimeout, body.WSIdleTimeout, body.HomeHibernateAfter, body.HomeBackupEvery} {
		if v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_duration", "invalid idle timeout: " + v})
				return
			}
		}
	}
	t, ok, err := a.cp.Store().GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	// ⚠️ The blob is ENCODED by the CP (limits.go's tenantLimits), not here — Limits is
	// only the projection that carries these values across the seam. Whichever side
	// holds the json tags has to be the only one, or a field added there would be
	// dropped on every save from here.
	lim := Limits{
		MaxWorkspaces:      body.MaxWorkspaces,
		MaxSessions:        body.MaxSessions,
		MaxGitRepos:        body.MaxGitRepos,
		MaxLFSBytes:        body.MaxLFSBytes,
		MaxWorkspaceMem:    body.MaxWorkspaceMem,
		MaxWorkspaceCPU:    body.MaxWorkspaceCPU,
		MaxWorkspaceDiskGB: body.MaxWorkspaceDiskGB,
		AllowedSlotClasses: body.AllowedSlotClasses,
		// ⚠️ SlotClass is NOT in the body: this handler rewrites the whole limits blob,
		// so leaving it out of the struct would erase the tenant_admin's default on
		// every super_admin edit. Carried over from what is stored.
		SlotClass:                    a.tenantLimitsFor(r, t.ID).SlotClass,
		SessionIdleTimeout:           body.SessionIdleTimeout,
		InteractionIdleTimeout:       body.InteractionIdleTimeout,
		WSIdleTimeout:                body.WSIdleTimeout,
		HomeHibernateAfter:           body.HomeHibernateAfter,
		HomeBackupEvery:              body.HomeBackupEvery,
		AllowAgentSelfUpdate:         body.AllowAgentSelfUpdate,
		AllowEngineIngest:            body.AllowEngineIngest,
		TerminalHistoryRetentionDays: body.TerminalHistoryRetentionDays,
		AllowEngineLLM:               body.AllowEngineLLM,
		AllowEngineImage:             body.AllowEngineImage,
	}
	if err := a.cp.StoreTenantLimits(r.Context(), t.ID, lim); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Rebuild cached runtimes for this tenant so the new gate reaches the next
	// container start (the gate is injected as env when the runtime is built).
	a.cp.EvictTenantCache(t.ID)
	// ADR 0084 decision 9: push the change to the tenant's running workspaces at once, or
	// a grant revoked here stays live in the Agent's opencode config and gateway cache for
	// up to ten minutes — decision 8 exists specifically to rule that window out.
	a.cp.PushEngineCatalogChanged(r.Context(), t.ID, "tenant limits changed")
	// This endpoint had NO audit trail at all before ADR 0084 — every other admin write in
	// this file does. A switch that can take image generation and self-hosted chat away
	// from a whole tenant needs one; MaxWorkspaces etc. changing silently was already a
	// gap, but this ADR is only fixing the capability-removal switches it is adding.
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.limits", Target: t.Slug, At: store.NowTS(),
	})
	// ⚠️ A WARNING, not a gate, and the save above has already happened. Three reasons:
	//
	//  1. It is not an invariant this endpoint can hold. Ec2MaxSlots is CP env and the
	//     quotas are rows; an operator lowering the cap makes no API call at all, so a
	//     400 here would guard one direction of a two-sided relationship and read as a
	//     guarantee it cannot give.
	//  2. Over-subscription is a legitimate policy. max_workspaces bounds CONCURRENT
	//     workspaces, and tenants that never peak together are exactly who you would
	//     over-subscribe on purpose.
	//  3. A deployment that already exceeds it would be frozen out of editing anything
	//     else on this screen — terminal history retention, an idle timeout — by a
	//     condition the admin did not create and cannot fix from here.
	//
	// Computed AFTER the write and with no override, so what comes back describes the
	// state that now exists rather than a prediction about it.
	resp := map[string]any{
		"tenant": t.Slug, "max_workspaces": body.MaxWorkspaces, "max_sessions": body.MaxSessions,
		"max_workspace_mem":     body.MaxWorkspaceMem,
		"max_workspace_cpu":     body.MaxWorkspaceCPU,
		"max_workspace_disk_gb": body.MaxWorkspaceDiskGB,
		"allowed_slot_classes":  body.AllowedSlotClasses,
		"session_idle_timeout":  body.SessionIdleTimeout, "ws_idle_timeout": body.WSIdleTimeout,
		"interaction_idle_timeout":        body.InteractionIdleTimeout,
		"home_hibernate_after":            body.HomeHibernateAfter,
		"home_backup_every":               body.HomeBackupEvery,
		"allow_agent_self_update":         body.AllowAgentSelfUpdate,
		"allow_engine_ingest":             body.AllowEngineIngest,
		"terminal_history_retention_days": body.TerminalHistoryRetentionDays,
		"allow_engine_llm":                body.AllowEngineLLM,
		"allow_engine_image":              body.AllowEngineImage,
	}
	// A failure to read it is not a reason to fail the save — the save is done, and the
	// budget is advice. Say nothing rather than something wrong.
	if b, ok, err := a.cp.PoolBudget(r.Context(), "", 0); ok && err == nil && !b.OK() {
		resp["pool_budget"] = b
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetUserLimit (PUT /api/admin/user-limits) — per-membership override.
func (a Admin) SetUserLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		UserKey     string `json:"user_key"`
		TenantSlug  string `json:"tenant_slug"`
		MaxSessions int    `json:"max_sessions"`
		DiskGB      int    `json:"disk_gb"`
		// MemLimit is the per-workspace RAM cap in BYTES (0 = unset → deployment
		// default). Clamped to the tenant cap + host ceiling at container start; the
		// response echoes the effective (post-clamp) value so the admin sees it.
		MemLimit int64 `json:"mem_limit"`
		// CPULimit is the per-workspace CPU cap in Fargate CPU units (1024 = 1 vCPU),
		// independent of MemLimit. 0 = unset (ADR 0044 decision 1).
		CPULimit int `json:"cpu_limit"`
		// SlotClass is which machine class this member lands on ("" = the tenant
		// default). Not a size — the three numbers above still decide that (docs/log/70).
		SlotClass string `json:"slot_class"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := a.cp.TenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	key := body.UserKey
	if key == "" {
		key = a.cp.SanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), key)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "user is not a member of " + t.Slug})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "user is not a member of " + t.Slug})
		return
	}
	q := store.UserQuota{
		MaxSessions: body.MaxSessions, DiskGB: body.DiskGB,
		MemLimit: body.MemLimit, CPULimit: body.CPULimit, SlotClass: strings.TrimSpace(body.SlotClass),
	}
	if err := a.cp.Store().PutUserLimit(r.Context(), mem.ID, q); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The size axes feed the built runtime (docker --memory/--cpus, ECS task size +
	// ephemeral storage), so drop the cached runtime — the new values reach the next
	// container start. Also compute the effective post-clamp values so the caller sees
	// what will actually be applied rather than what they typed.
	a.cp.EvictMembershipCache(mem.ID)
	effMem, effCPU, effDisk := a.cp.ResolveWorkspaceSize(r.Context(), store.Workspace{MembershipID: mem.ID, TenantID: t.ID})
	effClass, classNote := a.cp.ResolveSlotClass(r.Context(), store.Workspace{MembershipID: mem.ID, TenantID: t.ID})
	resp := map[string]any{
		"user_key": key, "tenant": t.Slug, "max_sessions": body.MaxSessions, "disk_gb": body.DiskGB,
		"mem_limit": body.MemLimit, "mem_effective": effMem,
		"cpu_limit": body.CPULimit, "cpu_effective": effCPU, "disk_effective": effDisk,
		// The class the member will actually land on, and why it is not what was asked
		// for when it is not. A substituted class is otherwise invisible until the bill.
		"slot_class": q.SlotClass, "slot_class_effective": effClass, "slot_class_note": classNote,
	}
	// The disk axis is the one that also reaches a resource that ALREADY EXISTS: on the
	// EC2 slot pool it is the member's persistent home, and raising the number grows that
	// volume online. AFTER the save, so the stored row is the admin's intent either way,
	// and reported rather than enforced — see resizeHomeByMembership.
	//
	// The error is dropped on purpose and the note simply omitted: a lookup that failed
	// is not a reason to fail a save that succeeded, and saying nothing beats saying
	// something wrong about somebody's disk.
	if hr, err := a.cp.ResizeHomeByMembership(r.Context(), mem.ID); err == nil && hr.Outcome != "" {
		resp["home_resize"] = hr
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetMembershipRole (PUT /api/admin/membership-role
// {tenant_slug, user_key, role}) grants or revokes a member's tenant-scoped admin
// role (member | tenant_admin). super_admin only: minting a tenant_admin is a
// privilege escalation kept to the deployment operator (a tenant_admin cannot
// promote others). Deployment-wide super_admin stays env-only (SUPER_ADMIN_EMAILS).
func (a Admin) SetMembershipRole(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	role := "member"
	if body.Role == "tenant_admin" {
		role = "tenant_admin"
	}
	t, ok, err := a.cp.Store().GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	if body.UserKey == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "user_key required"})
		return
	}
	ident, found, err := a.cp.Store().GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member of " + t.Slug})
		return
	}
	mem, ok, err := a.cp.Store().GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_membership", "not a member of " + t.Slug})
		return
	}
	if err := a.cp.Store().SetMembershipRole(r.Context(), mem.ID, role); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_key": body.UserKey, "tenant": t.Slug, "role": role})
}

// PoolStatus (GET /api/admin/ec2-pool) reports the EC2 slot pool: how many boxes are
// provisioned, which are asleep, whose home is on which one, what is hibernating, and
// whether the golden snapshot still matches the running image (docs/log/64 §64.18.6).
//
// super_admin only. This is deployment infrastructure — slots are shared across tenants,
// so there is no view of it that belongs to one tenant_admin.
//
// On every other runtime profile it answers {"runtime": ...} with no pool, and the
// Console hides the screen. Reporting an empty pool instead would read as "your slots all
// vanished" on a Fargate deployment.
func (a Admin) PoolStatus(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	st, ok, err := a.cp.PoolStatus(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"runtime": "other"})
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// TerminatePoolSlot (DELETE /api/admin/ec2-pool/slots/{id}) removes one QUARANTINED slot.
// Nothing else in the product ever does: a box that could not mount a home is stopped and
// kept as evidence (ADR 0045 decision 20), and both sweeper walks skip it — so until this
// existed the screen could say "terminate it once you have what you need" while offering no
// way to, and its root volume billed for the life of the deployment.
//
// super_admin only, like the screen it sits on: slots are shared across tenants, so no
// tenant_admin owns one.
//
// The adapter, not this handler, decides what may be deleted (it re-reads the tags from AWS
// — ADR 0012). Two of its refusals are the operator's own mistake rather than a failure, so
// they come back as 404/409 with the reason: an id that is not a slot of this pool, and a
// slot that is a WORKING one. A live slot leaves through the dormancy series; this route
// must not become a way to delete a machine somebody is sitting on.
func (a Admin) TerminatePoolSlot(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "instance id required"})
		return
	}
	// Before the intent write: a runtime with no pool has nothing to terminate, and must say
	// so even when the database is down rather than blame the audit log.
	if !a.cp.HasSlotPool() {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: "pool.slot_terminate", Target: id,
	})
	if !ok {
		return
	}
	reason, ok, err := a.cp.TerminateQuarantinedSlot(r.Context(), id)
	if !ok {
		refuseIrreversible(w, r, in, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	switch {
	case errors.Is(err, runtime.ErrSlotNotFound):
		refuseIrreversible(w, r, in, &APIError{http.StatusNotFound, "no_such_slot", err.Error()})
		return
	case errors.Is(err, runtime.ErrSlotNotQuarantined):
		refuseIrreversible(w, r, in, &APIError{http.StatusConflict, "slot_not_quarantined", err.Error()})
		return
	case errors.Is(err, runtime.ErrSlotInUse):
		refuseIrreversible(w, r, in, &APIError{http.StatusConflict, "slot_in_use", err.Error()})
		return
	case err != nil:
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	// The quarantine reason lived in the instance's tags, and the instance is going away.
	// Keeping it here is what lets decision 23's "the evidence is deliberately kept" still
	// hold after somebody presses the button.
	in.Done(r.Context(), "quarantined: "+reason, http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{"instance_id": id, "terminated": true, "quarantine_reason": reason})
}

// tenantLoginWire is the reply to PUT /api/admin/tenants/{slug}/login (the Console's
// `TenantLoginFields`, console/src/features/settings/tenant/tenantLoginTypes.ts).
//
// was: map[string]any{"tenant":…, "allowed_providers":…, "auto_join_domains":…,
//
//	"allowed_domains":…, "hidden_providers":…}
//
// All five keys are unconditional, so none of them takes omitempty. A CSV field is legally
// the empty string (meaning no restriction); with omitempty the key would vanish and "no
// restriction" would become indistinguishable from "unset".
//
// tenant echoes back the slug the Console already holds as a prop. `TenantLoginFields` does
// not declare it — and does not read it either.
type tenantLoginWire struct {
	Tenant           string `json:"tenant"`
	AllowedProviders string `json:"allowed_providers"`
	AutoJoinDomains  string `json:"auto_join_domains"`
	AllowedDomains   string `json:"allowed_domains"`
	HiddenProviders  string `json:"hidden_providers"`
}
