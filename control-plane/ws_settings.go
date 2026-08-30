package main

import (
	"encoding/json"
	"net/http"
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
}

// parseWSSettings unmarshals the stored JSON blob ("" => zero value).
func parseWSSettings(s string) wsSettings {
	var w wsSettings
	if s != "" {
		_ = json.Unmarshal([]byte(s), &w)
	}
	return w
}

// wsSettingsAPI は CP 管理のワークスペース設定の機能ハンドラ集（docs/log/23 残③）。
// 解決は埋め込みの memberAuth（登録側で withResolved に包む）。store は
// WorkspaceStore、tenants はオペレータゲート参照用 TenantStore の narrow view。
// キャッシュ破棄（evictMembershipCache）だけは memberAuth 経由の a.mgr を直接呼ぶ。
type wsSettingsAPI struct {
	memberAuth
	store   WorkspaceStore
	tenants TenantStore
}

func newWSSettingsAPI(m *manager) wsSettingsAPI {
	return wsSettingsAPI{memberAuth{m}, m.store, m.store}
}

// tenantAllowsAgentUpdate reports the operator gate for a workspace's tenant.
func (a wsSettingsAPI) tenantAllowsAgentUpdate(r *http.Request, ws Workspace) bool {
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
	raw, _ := a.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	})
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
	raw, _ := a.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	if v, ok := body["agentUpdate"].(bool); ok {
		st.AgentUpdate = v && a.tenantAllowsAgentUpdate(r, res.ws)
	}
	out, _ := json.Marshal(st)
	if err := a.store.SetWorkspaceSettings(r.Context(), res.ws.ID, string(out)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Drop the cached runtime so the next start rebuilds its env from the new setting.
	a.mgr.evictMembershipCache(res.ws.MembershipID)
	writeJSON(w, http.StatusOK, map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": a.tenantAllowsAgentUpdate(r, res.ws),
	})
}
