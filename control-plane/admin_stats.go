package main

import (
	"net/http"
)

// gib / mib / kib are defined in mem.go (package-wide 1024-based size constants).

// resolveMember maps a {slug, user_key} pair to its membership and (if one
// exists) its workspace record — the shared lookup for the admin per-member
// stats/sessions views. Mirrors the resolution in stop-workspace/clean-home
// (UpsertIdentity → GetMembership → GetWorkspaceByMembership). hasWS is false
// when the member has never started a workspace.
func (a adminAPI) resolveMember(r *http.Request, slug, key string) (mem Membership, ws Workspace, hasWS bool, aerr *apiError) {
	ctx := r.Context()
	t, ok, err := a.mgr.store.GetTenantBySlug(ctx, slug)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	if !ok {
		return Membership{}, Workspace{}, false, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"}
	}
	ident, ok, err := a.mgr.store.GetIdentityByUserKey(ctx, key)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	if !ok {
		return Membership{}, Workspace{}, false, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	mem, ok, err = a.mgr.store.GetMembership(ctx, ident.ID, t.ID)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	if !ok {
		return Membership{}, Workspace{}, false, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	ws, hasWS, err = a.mgr.store.GetWorkspaceByMembership(ctx, mem.ID)
	if err != nil {
		return Membership{}, Workspace{}, false, internalErr(err)
	}
	return mem, ws, hasWS, nil
}

// memberStats (GET /api/admin/tenants/{slug}/members/{key}/stats)
// returns a member's live Workspace resource usage: mem/CPU from cgroup and disk
// from `du` on the home tree, plus the disk quota if one is set. Disk is reported
// even while the container is stopped (the home path persists on the host).
func (a adminAPI) memberStats(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, ws, hasWS, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !hasWS {
		writeJSON(w, http.StatusOK, map[string]any{"running": false})
		return
	}
	out := containerStats(r.Context(), ws.ContainerName)
	// ⚠️ containerStats reads the host's cgroup through `docker inspect`, so on every
	// ECS profile (no docker binary in the CP task) it answers running:false for a
	// workspace that is plainly up — and the Console disables "force stop" on exactly
	// that field, which made the button permanently unusable there (docs/64 §64.27).
	// Ask the runtime whenever the docker read says "not running": on docker the two
	// agree, and everywhere else this is the only source that knows.
	if out["running"] != true {
		switch a.mgr.runtimeFor(ws, "").State(r.Context()) {
		case "running":
			out["running"] = true
		case "starting":
			out["starting"] = true
		}
	}
	if used, ok := dirDiskUsage(r.Context(), a.mgr.rootedDataDir(ws)); ok {
		out["disk_used"] = used
	}
	if ul, ok, _ := a.mgr.store.GetUserLimit(r.Context(), mem.ID); ok && ul.DiskGB > 0 {
		out["disk_quota"] = uint64(ul.DiskGB) * gib
	}
	writeJSON(w, http.StatusOK, out)
}

// memberSessions (GET /api/admin/tenants/{slug}/members/{key}/sessions)
// lists a member's sessions (read-only). Mirrors workspaceAPI.sessionsList but keyed by
// membership: the Agent is authoritative while the container runs (and we refresh
// the DB mirror); otherwise serve the last mirrored list as stopped.
func (a adminAPI) memberSessions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	_, ws, hasWS, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !hasWS {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}})
		return
	}
	ctx := r.Context()
	rt := a.mgr.runtimeFor(ws, "")
	if rt.State(ctx) == "running" {
		if list, err := a.mgr.agentSessions(ctx, rt); err == nil {
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
			_ = a.mgr.store.ReplaceSessions(ctx, ws.ID, rows)
			writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
			return
		}
		// Agent unreachable (e.g. mid-start): fall through to the DB mirror.
	}
	rows, err := a.mgr.store.ListSessions(ctx, ws.ID)
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
