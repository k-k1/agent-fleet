package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Per-workspace member settings owned by the Control Plane. Unlike the toolchain
// selection (which the in-container Agent reads/writes), these live in the CP DB, so
// they can be edited while the container is STOPPED and are applied at the next
// container start by mapping them to env (see manager.workspaceExtraEnv). This is the
// home for "apply on next start" preferences; add a field + an env mapping as they
// grow — the generic GET/PUT below carries any known key.
type wsSettings struct {
	// AgentUpdate: opt in to updating the baked CLIs (claude/opencode/codex) to
	// latest at container start. Operator-gated by the tenant's allow_agent_self_update
	// (workspaceExtraEnv only emits AF_AGENT_SELF_UPDATE=1 when the tenant allows it).
	AgentUpdate bool `json:"agentUpdate"`

	// --- Preview subdomains (docs/log/81) ---------------------------------------
	// PreviewPorts are the ports the host-based preview may expose (empty = the default
	// 3000,8080). A subdomain for a port not listed answers 404 rather than "not allowed":
	// it does not even admit that it exists, so which ports are open cannot be probed from
	// outside (ADR 0062 decision 6).
	PreviewPorts []int `json:"previewPorts,omitempty"`
	// PreviewFixedSlug: keep this workspace's slug instead of drawing a new one at every
	// start (docs/log/81 §4.1). Default false, i.e. redrawn each time as required. There is
	// effectively one reason to turn it on: external IdP redirect URI registration
	// (NextAuth / Auth.js) accepts neither a prefix match nor a wildcard.
	PreviewFixedSlug bool `json:"previewFixedSlug,omitempty"`
	// PreviewPublic: openable without authentication (docs/log/81 §6.1). Reset to false at
	// every start (fail-closed) — nearly the only accident this feature has is forgetting
	// that it was left public, so forgetting closes it.
	PreviewPublic bool `json:"previewPublic,omitempty"`
	// PreviewTenantShare: show it to every active member of the same tenant (docs/log/81
	// §14). Authentication stays mandatory — that is the difference from public mode: not
	// "anyone" but "a colleague who can log in".
	//
	// Unlike PreviewPublic this is NOT reset to off at every start (ADR 0062 decision 14).
	// Fail-closed is needed against "left open to the world and forgotten", which does not
	// apply when the other party can already log in to the Console. The use (having someone
	// look over some days) always spans a restart, so resetting it each time would make the
	// setting as good as absent.
	PreviewTenantShare bool `json:"previewTenantShare,omitempty"`
	// PreviewReservedSlug is the reservation used only while PreviewFixedSlug is on. It has
	// to be distinct from the running slug (the workspace.preview_slug column): that column
	// always goes back to empty on stop (a stopped workspace's URL must not resolve), so
	// making it double as the reservation would produce "I fixed it and it changed on
	// restart anyway".
	PreviewReservedSlug string `json:"previewReservedSlug,omitempty"`
	// PreviewCrossOrigin: let sibling preview origins call each other (docs/log/81 §2.4).
	// On, the auth cookie becomes SameSite=None and CP supplies CORS for sibling origins of
	// the same slug only. Default off: allowing cross-origin by default would make it the
	// default that any third-party page that knows the URL can reach the preview through
	// the user's browser.
	PreviewCrossOrigin bool `json:"previewCrossOrigin,omitempty"`
}

// parseWSSettings unmarshals the stored JSON blob ("" => zero value).
func parseWSSettings(s string) wsSettings {
	var w wsSettings
	if s != "" {
		_ = json.Unmarshal([]byte(s), &w)
	}
	return w
}

// wsSettingsAPI is the handler set for the CP-owned workspace settings (docs/log/23 ③).
// Resolution comes from the embedded memberAuth (registration wraps it in withResolved).
// store is a narrow WorkspaceStore view and tenants a narrow TenantStore view for the
// operator gate; only cache eviction (evictMembershipCache) reaches a.mgr directly through
// memberAuth.
type wsSettingsAPI struct {
	memberAuth
	store   store.WorkspaceStore
	tenants store.TenantStore
}

func newWSSettingsAPI(m *manager) wsSettingsAPI {
	return wsSettingsAPI{memberAuth{m}, m.store, m.store}
}

// tenantAllowsAgentUpdate reports the operator gate for a workspace's tenant.
func (a wsSettingsAPI) tenantAllowsAgentUpdate(r *http.Request, ws store.Workspace) bool {
	t, err := a.tenants.GetTenant(r.Context(), ws.TenantID)
	if err != nil {
		return false
	}
	return parseLimits(t.Limits).AllowAgentSelfUpdate
}

// get (GET /api/env/ws-settings) returns the workspace's CP-owned
// settings plus the relevant operator gates. DB-backed, so it works whether the
// container is running or stopped.
func (a wsSettingsAPI) get(w http.ResponseWriter, r *http.Request, res *resolved) {
	raw := mustSettings(r.Context(), a.mgr, res.ws.ID)
	st := parseWSSettings(raw)
	out := map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	}
	a.addPreview(r, res, st, out)
	writeJSON(w, http.StatusOK, out)
}

// addPreview appends the preview-subdomain block (docs/log/81). The issued URLs are read
// from the STORE rather than res.ws: the resolved workspace comes from the runtime
// cache, which was built before this container start rotated the slug, so trusting it
// would show the previous start's (now dead) URLs.
func (a wsSettingsAPI) addPreview(r *http.Request, res *resolved, st wsSettings, out map[string]any) {
	domain := a.mgr.previewDomain
	out["previewDomain"] = domain
	out["previewPorts"] = previewPortsOf(st)
	out["previewFixedSlug"] = st.PreviewFixedSlug
	out["previewPublic"] = st.PreviewPublic
	out["previewTenantShare"] = st.PreviewTenantShare
	out["previewCrossOrigin"] = st.PreviewCrossOrigin
	out["previewMaxPorts"] = maxPreviewPorts
	urls := map[string]string{}
	// Stable links for sharing (docs/log/81 §14.6). These ARE returned while the workspace
	// is stopped: being valid across starts is their whole reason to exist, so dropping
	// them because it happens to be stopped right now removes the very property of being a
	// link you can paste.
	shareLinks := map[string]string{}
	if domain != "" {
		for _, p := range previewPortsOf(st) {
			if link := previewOpenPathFor(res.ident.UserKey, p); link != "" {
				shareLinks[strconv.Itoa(p)] = link
			}
		}
		if ws, ok, err := a.store.GetWorkspaceByMembership(r.Context(), res.ws.MembershipID); err == nil && ok && ws.PreviewSlug != "" {
			for _, p := range previewPortsOf(st) {
				urls[strconv.Itoa(p)] = previewURLFor(ws.PreviewSlug, p, domain)
			}
		}
	}
	// Left empty while stopped: never show a URL that has not been issued — a link that
	// 404s when pressed comes back as a report that the feature is broken.
	out["previewUrls"] = urls
	out["previewShareLinks"] = shareLinks
}

// sharedPreviews (GET /api/preview/shared) lists the OTHER workspaces in this tenant
// that are currently sharing their preview (docs/log/81 §14.6). It is what turns "someone
// told me a URL once" into something findable.
//
// Running-ness is decided by the presence of the preview_slug column, not by asking the
// runtime per workspace: that would add one round trip per tenant member, and what is
// wanted here is not "is the container up" but "is there a preview URL right now".
func (a wsSettingsAPI) sharedPreviews(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	ctx := r.Context()
	domain := a.mgr.previewDomain
	items := []map[string]any{}
	if domain == "" { // no host-based preview here: empty, and Console hides the section
		writeJSON(w, http.StatusOK, map[string]any{"domain": "", "items": items})
		return
	}
	// The member list is not on the narrow WorkspaceStore, so read it from the store itself.
	members, err := a.mgr.store.ListMembersByTenant(ctx, mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	byMembership := make(map[string]store.MemberInfo, len(members))
	for _, m := range members {
		byMembership[m.MembershipID] = m
	}
	workspaces, err := a.store.ListWorkspaces(ctx, mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, ws := range workspaces {
		if ws.MembershipID == mv.MembershipID {
			continue // one's own is already in the top half of the popover
		}
		owner, ok := byMembership[ws.MembershipID]
		if !ok {
			continue // do not list the workspace of someone who is no longer a member
		}
		// This reads settings once per tenant member. Acceptable because it only runs the
		// moment the popover is opened, but this is the place that turns into a batched
		// read once tenants grow.
		st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
		if !st.PreviewTenantShare {
			continue
		}
		ports := previewPortsOf(st)
		urls := map[string]string{}
		links := map[string]string{}
		for _, p := range ports {
			links[strconv.Itoa(p)] = previewOpenPathFor(owner.UserKey, p)
			if ws.PreviewSlug != "" {
				urls[strconv.Itoa(p)] = previewURLFor(ws.PreviewSlug, p, domain)
			}
		}
		items = append(items, map[string]any{
			"ownerUserKey": owner.UserKey,
			"ownerEmail":   owner.Email,
			"ports":        ports,
			"urls":         urls, // empty while stopped: never show a URL that was not issued
			"shareLinks":   links,
			"running":      ws.PreviewSlug != "",
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["ownerUserKey"].(string) < items[j]["ownerUserKey"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"domain": domain, "items": items})
}

// reissuePreview (POST /api/env/ws-settings/preview/reissue) throws this workspace's
// preview URLs away and mints new ones — the remedy for "I pasted the URL somewhere I
// should not have".
//
// A running container is not recreated, so `AF_PREVIEW_URL_*` inside it stays stale until
// the next start. Immediate revocation still wins: telling the user "it will change at the
// next restart" while the URL they leaked keeps working is not a remedy.
func (a wsSettingsAPI) reissuePreview(w http.ResponseWriter, r *http.Request, res *resolved) {
	if a.mgr.previewDomain == "" {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_configured", "preview subdomains are not configured"})
		return
	}
	ws, ok, err := a.store.GetWorkspaceByMembership(r.Context(), res.ws.MembershipID)
	if err != nil || !ok {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "internal", "workspace lookup failed"})
		return
	}
	raw := mustSettings(r.Context(), a.mgr, ws.ID)
	st := parseWSSettings(raw)
	// Throw the reservation away too (the carry-over used when fixed slug is on). Kept, it
	// would hand the supposedly discarded URL back at the next start.
	if st.PreviewReservedSlug != "" {
		st.PreviewReservedSlug = ""
		out, _ := json.Marshal(st)
		if err := a.store.SetWorkspaceSettings(r.Context(), ws.ID, string(out)); err != nil {
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "internal", "save failed"})
			return
		}
	}
	// Redraw on the spot only while running (i.e. a slug has been issued). While stopped
	// none exists, so discarding the reservation already did the job.
	rotated := false
	if ws.PreviewSlug != "" {
		if _, err := a.mgr.rotatePreviewSlug(r.Context(), ws); err != nil {
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "internal", "could not mint a new preview slug"})
			return
		}
		rotated = true
	}
	a.mgr.evictMembershipCache(ws.MembershipID)
	raw = mustSettings(r.Context(), a.mgr, ws.ID)
	body := map[string]any{
		"agentUpdate":      parseWSSettings(raw).AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
		// Whether the URLs actually changed on the spot. A stopped workspace has no issued
		// slug, so this call succeeds while nothing on screen changes; without a way to
		// tell the two apart, the caller reports the success as "pressing it does nothing"
		// (which is how it was reported).
		"previewReissued": rotated,
	}
	a.addPreview(r, res, parseWSSettings(raw), body)
	writeJSON(w, http.StatusOK, body)
}

// put (PUT /api/env/ws-settings) merges the posted known keys into the
// workspace's stored JSON. Works while the container is stopped; the value takes
// effect at the next container start. Only known keys are honored, and agentUpdate is
// gated by the tenant policy (a member can't enable it when the operator forbids it).
func (a wsSettingsAPI) put(w http.ResponseWriter, r *http.Request, res *resolved) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	raw := mustSettings(r.Context(), a.mgr, res.ws.ID)
	st := parseWSSettings(raw)
	if v, ok := body["agentUpdate"].(bool); ok {
		st.AgentUpdate = v && a.tenantAllowsAgentUpdate(r, res.ws)
	}
	// Preview (docs/log/81). The values are stored even on a deployment without host-based
	// preview (AF_PREVIEW_DOMAIN unset): a setting that merely has no effect is easier to
	// explain than one that silently disappears because of how the deployment is built.
	if raw, ok := body["previewPorts"].([]any); ok {
		ports := make([]int, 0, len(raw))
		for _, v := range raw {
			if f, ok := v.(float64); ok {
				ports = append(ports, int(f))
			}
		}
		st.PreviewPorts = sanitizePreviewPorts(ports)
	}
	if v, ok := body["previewFixedSlug"].(bool); ok {
		st.PreviewFixedSlug = v
	}
	if v, ok := body["previewCrossOrigin"].(bool); ok {
		st.PreviewCrossOrigin = v
	}
	if v, ok := body["previewPublic"].(bool); ok {
		st.PreviewPublic = v
		auditPreviewPublic(r.Context(), a.mgr, res, v)
	}
	if v, ok := body["previewTenantShare"].(bool); ok && v != st.PreviewTenantShare {
		st.PreviewTenantShare = v
		auditPreviewShare(r.Context(), a.mgr, res, v)
	}
	out, _ := json.Marshal(st)
	if err := a.store.SetWorkspaceSettings(r.Context(), res.ws.ID, string(out)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Drop the cached runtime so the next start rebuilds its env from the new setting.
	a.mgr.evictMembershipCache(res.ws.MembershipID)
	body2 := map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	}
	a.addPreview(r, res, st, body2)
	writeJSON(w, http.StatusOK, body2)
}
