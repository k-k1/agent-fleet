package main

import (
	"net/http"
)

const gib = 1024 * 1024 * 1024

// adminResolveMember maps a {slug, user_key} pair to its membership and (if one
// exists) its workspace record — the shared lookup for the admin per-member
// stats/sessions views. Mirrors the resolution in stop-workspace/clean-home
// (UpsertIdentity → GetMembership → GetWorkspaceByMembership). hasWS is false
// when the member has never started a workspace.
func (c config) adminResolveMember(r *http.Request, slug, key string) (mem Membership, ws Workspace, hasWS bool, aerr *apiError) {
	ctx := r.Context()
	t, ok, err := c.mgr.store.GetTenantBySlug(ctx, slug)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	if !ok {
		return Membership{}, Workspace{}, false, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"}
	}
	ident, err := c.mgr.store.UpsertIdentity(ctx, "", key, "")
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	mem, ok, err = c.mgr.store.GetMembership(ctx, ident.ID, t.ID)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	if !ok {
		return Membership{}, Workspace{}, false, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	ws, hasWS, err = c.mgr.store.GetWorkspaceByMembership(ctx, mem.ID)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	return mem, ws, hasWS, nil
}

// handleAdminMemberStats (GET /api/admin/tenants/{slug}/members/{key}/stats)
// returns a member's live Workspace resource usage: mem/CPU from cgroup and disk
// from `du` on the home tree, plus the disk quota if one is set. Disk is reported
// even while the container is stopped (the home path persists on the host).
func (c config) handleAdminMemberStats(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := c.requireTenantAdmin(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, ws, hasWS, aerr := c.adminResolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !hasWS {
		writeJSON(w, http.StatusOK, map[string]any{"running": false})
		return
	}
	out := containerStats(r.Context(), ws.ContainerName)
	if used, ok := dirDiskUsage(r.Context(), c.mgr.rootedDataDir(ws)); ok {
		out["disk_used"] = used
	}
	if ul, ok, _ := c.mgr.store.GetUserLimit(r.Context(), mem.ID); ok && ul.DiskGB > 0 {
		out["disk_quota"] = uint64(ul.DiskGB) * gib
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminMemberSessions (GET /api/admin/tenants/{slug}/members/{key}/sessions)
// lists a member's sessions (read-only). Mirrors workspaceAPI.sessionsList but keyed by
// membership: the Agent is authoritative while the container runs (and we refresh
// the DB mirror); otherwise serve the last mirrored list as stopped.
func (c config) handleAdminMemberSessions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := c.requireTenantAdmin(w, r, r.PathValue("slug")); !ok {
		return
	}
	_, ws, hasWS, aerr := c.adminResolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !hasWS {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}})
		return
	}
	ctx := r.Context()
	rt := c.mgr.runtimeFor(ws, "")
	if rt.State(ctx) == "running" {
		if list, err := c.mgr.agentSessions(ctx, rt); err == nil {
			rows := make([]SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, SessionRow{
					Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
					Label: s.Label, CreatedAt: s.CreatedAt, State: state,
				})
			}
			_ = c.mgr.store.ReplaceSessions(ctx, ws.ID, rows)
			writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
			return
		}
		// Agent unreachable (e.g. mid-start): fall through to the DB mirror.
	}
	rows, err := c.mgr.store.ListSessions(ctx, ws.ID)
	if err != nil {
		rows = nil
	}
	out := make([]sessionWire, 0, len(rows))
	for _, r0 := range rows {
		out = append(out, sessionWire{
			Name: r0.Name, Kind: r0.Kind, Dir: r0.Dir, Repo: r0.Repo, Label: r0.Label,
			Started: fmtStarted(r0.CreatedAt), CreatedAt: r0.CreatedAt, Alive: false,
			Resumable: true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}
