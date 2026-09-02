package main

import (
	"net/http"
	"sync"

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
	rt := a.mgr.runtimeFor(ws, "")
	// running / mem / CPU は runtime 中立の合成に任せる（metrics.go の workspaceStats）:
	// ホストの cgroup が読める構成ならそれを、読めない構成（ECS 全般）なら Agent が
	// 自分の cgroup から読んだ値を載せる。State() は docker の読みが空振ったときだけ
	// 引く（メンバー詳細は 4 秒ごとにポーリングされる画面なので、docker 構成で毎回
	// `docker inspect` を 2 度走らせないための遅延評価）。
	out := workspaceStats(ctx, a.mgr, rt, sync.OnceValue(func() string { return rt.State(ctx) }))
	// ディスクはホスト側の du を優先する。CP と Workspace が同じホストに載っている
	// 構成では、これが「ホーム木そのものの大きさ」——コンテナが止まっていても読める
	// 唯一の数字で、停止中の棚卸しに要る。ECS では対象のパスが CP に無いので、
	// Agent が statfs で返した disk_used / disk_total（永続 home の EBS そのもの）が
	// 既に out に載っている。
	if used, ok := dirDiskUsage(ctx, a.mgr.rootedDataDir(ws)); ok {
		out["disk_used"] = used
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
	rt := a.mgr.runtimeFor(ws, "")
	if rt.State(ctx) == "running" {
		if list, err := a.mgr.agentSessions(ctx, rt); err == nil {
			rows := make([]store.SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, store.SessionRow{
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
