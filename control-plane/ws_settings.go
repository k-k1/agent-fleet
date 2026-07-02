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

// tenantAllowsAgentUpdate reports the operator gate for a workspace's tenant.
func (c config) tenantAllowsAgentUpdate(r *http.Request, ws Workspace) bool {
	t, err := c.mgr.store.GetTenant(r.Context(), ws.TenantID)
	if err != nil {
		return false
	}
	return parseLimits(t.Limits).AllowAgentSelfUpdate
}

// handleWSSettingsGet (GET /api/env/ws-settings) returns the workspace's CP-owned
// settings plus the relevant operator gates. DB-backed, so it works whether the
// container is running or stopped.
func (c config) handleWSSettingsGet(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	raw, _ := c.mgr.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": c.tenantAllowsAgentUpdate(r, res.ws),
	})
}

// handleWSSettingsPut (PUT /api/env/ws-settings) merges the posted known keys into the
// workspace's stored JSON. Works while the container is stopped; the value takes
// effect at the next container start. Only known keys are honored, and agentUpdate is
// gated by the tenant policy (a member can't enable it when the operator forbids it).
func (c config) handleWSSettingsPut(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	raw, _ := c.mgr.store.GetWorkspaceSettings(r.Context(), res.ws.ID)
	st := parseWSSettings(raw)
	if v, ok := body["agentUpdate"].(bool); ok {
		st.AgentUpdate = v && c.tenantAllowsAgentUpdate(r, res.ws)
	}
	out, _ := json.Marshal(st)
	if err := c.mgr.store.SetWorkspaceSettings(r.Context(), res.ws.ID, string(out)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	// Drop the cached runtime so the next start rebuilds its env from the new setting.
	c.mgr.evictMembershipCache(res.ws.MembershipID)
	writeJSON(w, http.StatusOK, map[string]any{
		"agentUpdate":      st.AgentUpdate,
		"allowAgentUpdate": c.tenantAllowsAgentUpdate(r, res.ws),
	})
}
