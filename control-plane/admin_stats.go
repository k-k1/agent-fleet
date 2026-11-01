package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/adminnotice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// gib / mib / kib are defined in mem.go (package-wide 1024-based size constants).

// resolveMember maps a {slug, user_key} pair to its membership and (if one
// exists) its workspace record — the shared lookup for the admin per-member
// stats/sessions views. Mirrors the resolution in stop-workspace/clean-home
// (UpsertIdentity → GetMembership → GetWorkspaceByMembership). hasWS is false
// when the member has never started a workspace.
func (a adminAPI) resolveMember(r *http.Request, slug, key string) (mem store.Membership, ws store.Workspace, hasWS bool, aerr *apiError) {
	ctx := r.Context()
	t, ok, err := a.mgr.store.GetTenantBySlug(ctx, slug)
	if err != nil {
		return store.Membership{}, store.Workspace{}, false, internalErr(err)
	}
	if !ok {
		return store.Membership{}, store.Workspace{}, false, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"}
	}
	ident, ok, err := a.mgr.store.GetIdentityByUserKey(ctx, key)
	if err != nil {
		return store.Membership{}, store.Workspace{}, false, internalErr(err)
	}
	if !ok {
		return store.Membership{}, store.Workspace{}, false, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	mem, ok, err = a.mgr.store.GetMembership(ctx, ident.ID, t.ID)
	if err != nil {
		return store.Membership{}, store.Workspace{}, false, internalErr(err)
	}
	if !ok {
		return store.Membership{}, store.Workspace{}, false, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	ws, hasWS, err = a.mgr.store.GetWorkspaceByMembership(ctx, mem.ID)
	if err != nil {
		return store.Membership{}, store.Workspace{}, false, internalErr(err)
	}
	return mem, ws, hasWS, nil
}

// memberStats (GET /api/admin/tenants/{slug}/members/{key}/stats)
// returns a member's live Workspace resource usage: mem/CPU from cgroup and disk
// from `du` on the home tree, plus the disk quota if one is set. Disk is reported
// even while the container is stopped (the home path persists on the host).
//
// ?disk=0 skips the host-side du. The roster cards poll this for mem/CPU only and already
// show the usage sampler's disk reading, so a du per card per poll would be cost for
// nothing; absent (the member detail) keeps the full answer. Disk figures the Agent itself
// returned (ECS statfs) are not removed: they cost nothing extra here.
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
	ctx := r.Context()
	rt := a.mgr.runtimeFor(ws, noSecretKeys)
	// running / mem / CPU are left to the runtime-neutral composition (workspaceStats
	// in metrics.go): the host's cgroup where a deployment can read it, otherwise (ECS
	// in general) the value the Agent read from its own cgroup. State() is consulted
	// only when the docker read came up empty — the member detail screen polls every 4
	// seconds, and this laziness is what keeps a docker deployment from running
	// `docker inspect` twice on every poll.
	state := sync.OnceValue(func() string { return rt.State(ctx) })
	readStart := time.Now()
	out := workspaceStats(ctx, a.mgr, rt, state)
	// The member detail is opened from a roster snapshot and polls only this, so the
	// automatic-stop reason rides here: read from the snapshot, it outlived the restart
	// that cleared it. A running workspace has none, which spares that poll the State call.
	if out["running"] != true {
		if as := store.CurrentAutoStop(ctx, a.mgr.store, mem.ID, state()); as != nil {
			out["auto_stop"] = as
		}
	}
	// Which CLIs failed to install on the last start. A running workspace the Agent answered
	// for is authoritative (a clean start reports none); otherwise (stopped, unreachable, or a
	// local docker deployment where the host-side read never asks the Agent) it is the sampler's
	// stored outcome. The list stays out of the response when empty, and the detail renders only
	// this key, so a stale snapshot on the client can never decide it.
	live, answered := out["boot_install_failed"].(map[string]string)
	delete(out, "boot_install_failed")
	if answered {
		// Persist what was just seen, stamped with when the read began, so a stop before the
		// next sweep falls back to this outcome and not to an older one, and an older sweep
		// that lands later is refused by the same ordering guard. The record is "the last
		// start observed"; a start that began and ended between two reads is not seen at all.
		raw := ""
		if len(live) > 0 {
			if b, err := json.Marshal(live); err == nil {
				raw = string(b)
			}
		}
		if err := a.mgr.store.PutWorkspaceBootFailed(ctx, ws.ID, raw, store.DiskObservedAt(readStart)); err == nil {
			adminnotice.BootInstall(ctx, a.mgr.store, ws)
		}
	} else {
		live = nil
		if raw, err := a.mgr.store.GetWorkspaceBootFailed(ctx, ws.ID); err == nil && raw != "" {
			_ = json.Unmarshal([]byte(raw), &live)
		}
	}
	if len(live) > 0 {
		out["boot_failed"] = live
	}
	// Disk prefers the host-side du. Where CP and Workspace share a host, that is the
	// size of the home tree itself — the only figure readable while the container is
	// stopped, which taking stock of stopped workspaces needs. On ECS the path is not on
	// the CP at all, so out already carries the disk_used / disk_total the Agent
	// returned from statfs (the persistent home's own EBS volume).
	if r.URL.Query().Get("disk") != "0" {
		// Never waits on du: a 178 GiB home took 13 s to walk and the detail polls every
		// 4 s. The last sample is served with its age and a refresh runs in the
		// background; with nothing sampled yet the answer is "pending", not 0.
		if used, at, ok := dirDiskUsageNoWait(a.mgr.rootedDataDir(ws)); ok {
			out["disk_used"] = used
			out["disk_observed_at"] = at.UTC().Format(time.RFC3339)
		} else if _, agent := out["disk_used"]; !agent {
			out["disk_pending"] = true
		}
	}
	if ul, ok, _ := a.mgr.store.GetUserLimit(ctx, mem.ID); ok && ul.DiskGB > 0 {
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
	rt := a.mgr.runtimeFor(ws, noSecretKeys)
	if rt.State(ctx) == "running" {
		readStart := time.Now()
		if list, err := a.mgr.agentSessions(ctx, rt); err == nil {
			_ = a.mgr.mirrorSessions(ctx, ws.ID, readStart, list)
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
