package main

// mcp_wiring.go — MCP 家系（`internal/mcpsrv`）の**配線**だけを持つ。
// ウェーブ C の別名 alias_mcp.go は RECLAIM-C で回収し、呼び出し側は mcpsrv を直接呼ぶ。
// ここに残るのは別名ではなく、**mcpsrv → main の切断面**（`mcpsrv.CP` の実装 26 本と
// エラーの詰め替え 1 本）である。mcpsrv は main を import できないので、これが唯一の方法。

import (
	"context"
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

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
