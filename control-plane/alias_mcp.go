// alias_mcp.go — MCP 家系（internal/mcpsrv）の逆向きの継ぎ目、1 枚（ADR 0067 決定 1）。
//
// 実体は control-plane/internal/mcpsrv/ に移した。呼び出し側（routes.go /
// workspace_lifecycle.go / tenant_idp_api.go）は 1 行も変えない。ここが担うのは 3 つ:
//
//  1. cpDeps — mcpsrv.CP（deps.go の切断面）を *manager の上に実装するアダプタ。
//  2. mcpAPI / mcpServerAPI — routes.go が呼ぶ**非公開メソッド**（`m.adminList` など）を
//     持つ薄いラッパ。🔥 型エイリアス（`type mcpServerAPI = mcpsrv.ServerAPI`）では
//     済まない: 非公開メソッドはパッケージ境界を越えられないので、遠側で公開名にした
//     うえで、こちら側で元の綴りに包み直すしかない。
//  3. 素の別名 3 つ（MCPSignKey / MintMCPToken / MaskedValue）。
//
// エイリアス回収はウェーブ境界で別セッションが行う（ここでは何も回収しない）。

package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// --- 素の別名 -----------------------------------------------------------------------

var (
	mcpSignKey   = mcpsrv.MCPSignKey
	mintMCPToken = mcpsrv.MintMCPToken
)

const maskedValue = mcpsrv.MaskedValue

// --- 切断面のアダプタ ----------------------------------------------------------------

// cpDeps implements mcpsrv.CP over the CP manager. Every method is a one-liner onto
// something that already existed; nothing here is new behaviour.
type cpDeps struct{ m *manager }

var _ mcpsrv.CP = cpDeps{}

func (d cpDeps) Store() store.Store               { return d.m.store }
func (d cpDeps) Custodian() mcpsrv.KeyCustodian   { return d.m.custodian }
func (d cpDeps) Master32() []byte                 { return d.m.master32 }
func (d cpDeps) TokenSignMaster() []byte          { return d.m.tokenSignMaster() }
func (d cpDeps) EvictMembershipCache(mid string)  { d.m.evictMembershipCache(mid) }
func (d cpDeps) TenantSel(r *http.Request) string { return tenantSel(r) }
func (d cpDeps) ScopeRank(scope string) int       { return scopeRank(scope) }
func (d cpDeps) HashPAT(token string) string      { return hashPAT(token) }
func (d cpDeps) EgressDefaults() []string         { return defaultEgressAllowlist }
func (d cpDeps) StopWorkspaceByMembership(ctx context.Context, mid string) error {
	return d.m.stopWorkspaceByMembership(ctx, mid)
}

func (d cpDeps) RuntimeFor(ws store.Workspace, secretKey string, extraEnv ...string) runtime.Runtime {
	return d.m.runtimeFor(ws, secretKey, extraEnv...)
}

func (d cpDeps) WorkspaceStateByMembership(ctx context.Context, mid string) (string, string) {
	return d.m.workspaceStateByMembership(ctx, mid)
}

func (d cpDeps) ResolveWorkspaceSize(ctx context.Context, ws store.Workspace) (int64, int, int) {
	return d.m.resolveWorkspaceSize(ctx, ws)
}

func (d cpDeps) ResolveSlotClass(ctx context.Context, ws store.Workspace) (string, string) {
	return d.m.resolveSlotClass(ctx, ws)
}

func (d cpDeps) CountRunningInTenant(ctx context.Context, tenantID string) (int, error) {
	return d.m.countRunningInTenant(ctx, tenantID)
}

func (d cpDeps) HostStats() (float64, int, uint64, uint64) { return readHostStats() }

func (d cpDeps) TenantLimits(raw string) (maxWorkspaces, maxSessions int) {
	l := parseLimits(raw)
	return l.MaxWorkspaces, l.MaxSessions
}

func (d cpDeps) TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool) {
	return memberAuth{d.m}.tenantAdminFor(w, r, slug)
}

// ResolveByMembership converts the CP's per-request resolution to the two fields
// mcpsrv reads. A nil *resolved must stay nil on the far side — returning an empty
// Resolved would hand the tools a zero runtime instead of the error.
func (d cpDeps) ResolveByMembership(ctx context.Context, identityID, membershipID string) (*mcpsrv.Resolved, *mcpsrv.APIError) {
	res, aerr := d.m.resolveByMembership(ctx, identityID, membershipID)
	if res == nil {
		return nil, mcpAPIError(aerr)
	}
	return &mcpsrv.Resolved{RT: res.rt, MV: res.mv}, mcpAPIError(aerr)
}

func (d cpDeps) AgentSessions(ctx context.Context, rt runtime.Runtime) ([]mcpsrv.Session, error) {
	list, err := d.m.agentSessions(ctx, rt)
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.Session, 0, len(list))
	for _, s := range list {
		out = append(out, mcpsrv.Session{Name: s.Name, Display: s.Display, Kind: s.Kind, Label: s.Label, Alive: s.Alive})
	}
	return out, nil
}

func (d cpDeps) AgentText(ctx context.Context, rt runtime.Runtime, method, path string, body []byte) (string, error) {
	return agentText(ctx, rt, method, path, body)
}

func (d cpDeps) ScheduleGuardErr(ctx context.Context, membershipID, repoName, sessionName string) error {
	return scheduleGuardErr(ctx, d.m.store, membershipID, repoName, sessionName)
}

func (d cpDeps) MemoList(ctx context.Context, membershipID string) (any, error) {
	return memoListFor(ctx, d.m.store, membershipID)
}

func (d cpDeps) MemoCreate(ctx context.Context, mv store.MembershipView, repo, category, kind, body, refPath string) (any, error) {
	dto, aerr := memoCreateFor(ctx, d.m.store, mv, memoDTO{
		Repo: repo, Category: category, Kind: kind, Body: body, RefPath: refPath,
	})
	if aerr != nil {
		return nil, errors.New(aerr.message)
	}
	return dto, nil
}

func (d cpDeps) MemoUpdate(ctx context.Context, membershipID, id string, repo, category, body, refPath *string, position *int) (any, error) {
	dto, aerr := memoUpdateFor(ctx, d.m.store, membershipID, id, memoPatch{
		Repo: repo, Category: category, Body: body, RefPath: refPath, Position: position,
	})
	if aerr != nil {
		return nil, errors.New(aerr.message)
	}
	return dto, nil
}

func (d cpDeps) MemoFlush(ctx context.Context, rt runtime.Runtime, membershipID, sessionName string, ids []string) (map[string]any, error) {
	out, aerr := memoFlushFor(ctx, d.m.store, rt, membershipID, sessionName, ids, "")
	if aerr != nil {
		return nil, errors.New(aerr.message)
	}
	return out, nil
}

// mcpAPIError converts the CP's apiError (unexported fields) to the seam's copy.
func mcpAPIError(e *apiError) *mcpsrv.APIError {
	if e == nil {
		return nil
	}
	return &mcpsrv.APIError{Status: e.status, Code: e.code, Message: e.message}
}

// --- 呼び出し側が握る型（非公開メソッドの包み直し） -------------------------------------

// mcpAPI is the /mcp endpoint as routes.go names it.
type mcpAPI struct{ inner mcpsrv.API }

func newMCPAPI(m *manager) mcpAPI { return mcpAPI{mcpsrv.New(cpDeps{m})} }

func (a mcpAPI) handleMCP(w http.ResponseWriter, r *http.Request) { a.inner.HandleMCP(w, r) }

// mcpServerAPI is the tenant-distribution feature as routes.go names it.
type mcpServerAPI struct{ inner mcpsrv.ServerAPI }

func newMCPServerAPI(m *manager) mcpServerAPI { return mcpServerAPI{mcpsrv.NewServerAPI(cpDeps{m})} }

func (a mcpServerAPI) adminList(w http.ResponseWriter, r *http.Request)   { a.inner.AdminList(w, r) }
func (a mcpServerAPI) adminUpsert(w http.ResponseWriter, r *http.Request) { a.inner.AdminUpsert(w, r) }
func (a mcpServerAPI) adminDelete(w http.ResponseWriter, r *http.Request) { a.inner.AdminDelete(w, r) }

func (a mcpServerAPI) distribute(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	a.inner.Distribute(w, r, mv)
}

func (a mcpServerAPI) withMCPToken(h func(http.ResponseWriter, *http.Request, store.MembershipView)) http.HandlerFunc {
	return a.inner.WithMCPToken(h)
}
