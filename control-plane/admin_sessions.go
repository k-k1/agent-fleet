package main

import (
	"context"
	"log"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Deployment-wide session overview (P3-9 admin). A flat, cross-user list so an
// operator can see every running/resumable session at a glance without drilling
// into each member. Running workspaces are queried live from the Agent (the
// source of truth); stopped ones fall back to the DB mirror so their resumable
// sessions still appear.

// adminSessionRow is one session tagged with the member (tenant × user) that owns
// it and the owning workspace's docker state.
type adminSessionRow struct {
	Tenant         string `json:"tenant"`
	UserKey        string `json:"user_key"`
	Email          string `json:"email"`
	WorkspaceState string `json:"workspace_state"` // running | stopped | none
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Repo           string `json:"repo"`
	Dir            string `json:"dir"`
	State          string `json:"state"` // idle | working | question | stopped
	Alive          bool   `json:"alive"`
	Resumable      bool   `json:"resumable"`
	Started        string `json:"started"`
}

// allSessions (GET /api/admin/sessions?tenant=<slug>) serves the overview.
// super_admin spans every tenant; a tenant_admin is scoped to a tenant they
// administer (via ?tenant=), enforced by tenantScope.
func (a adminAPI) allSessions(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tenants, aerr := a.mgr.tenantsInScope(ctx, tenantID)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	out := make([]adminSessionRow, 0)
	for _, t := range tenants {
		members, err := a.mgr.store.ListMembersByTenant(ctx, t.ID)
		if err != nil {
			// Log the partial failure: a tenant must not vanish from the admin view in
			// silence.
			log.Printf("admin sessions: list members tenant=%s: %v", t.Slug, err)
			continue
		}
		byMembership := make(map[string]store.MemberInfo, len(members))
		for _, m := range members {
			byMembership[m.MembershipID] = m
		}
		wss, err := a.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("admin sessions: list workspaces tenant=%s: %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			mi := byMembership[ws.MembershipID]
			rt := a.mgr.runtimeFor(ws, "")
			state := rt.State(ctx)
			for _, s := range a.mgr.sessionsForOverview(ctx, ws, rt, state) {
				out = append(out, adminSessionRow{
					Tenant: t.Slug, UserKey: mi.UserKey, Email: mi.Email, WorkspaceState: state,
					Name: s.Name, Kind: s.Kind, Label: s.Label, Repo: s.Repo, Dir: s.Dir,
					State: s.State, Alive: s.Alive, Resumable: s.Resumable, Started: s.Started,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// tenantsInScope returns the tenants an admin view should span: exactly the one
// when tenantID is set (already gate-checked by tenantScope), else all.
func (m *manager) tenantsInScope(ctx context.Context, tenantID string) ([]store.Tenant, *apiError) {
	if tenantID != "" {
		t, err := m.store.GetTenant(ctx, tenantID)
		if err != nil {
			return nil, internalErr(err)
		}
		return []store.Tenant{t}, nil
	}
	ts, err := m.store.ListTenants(ctx)
	if err != nil {
		return nil, internalErr(err)
	}
	return ts, nil
}

// sessionsForOverview returns a workspace's sessions for the admin overview: live
// from the Agent when running (with a computed Started), else the DB mirror marked
// stopped/resumable. Mirrors workspaceAPI.sessionsList so the two views agree.
func (m *manager) sessionsForOverview(ctx context.Context, ws store.Workspace, rt runtime.Runtime, state string) []sessionWire {
	if state == "running" {
		if list, err := m.agentSessions(ctx, rt); err == nil {
			for i := range list {
				if list[i].Started == "" {
					list[i].Started = fmtStarted(list[i].CreatedAt)
				}
			}
			return list
		}
		// Agent unreachable (mid-start/unhealthy): fall through to the DB mirror.
	}
	rows, err := m.store.ListSessions(ctx, ws.ID)
	if err != nil {
		return nil
	}
	out := make([]sessionWire, 0, len(rows))
	for _, r0 := range rows {
		out = append(out, sessionWire{
			Name: r0.Name, Kind: r0.Kind, Dir: r0.Dir, Repo: r0.Repo, Label: r0.Label,
			Started: fmtStarted(r0.CreatedAt), CreatedAt: r0.CreatedAt,
			State: r0.State, Alive: false, Resumable: true,
		})
	}
	return out
}
