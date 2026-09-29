package main

// engine_access.go — the tenant_admin's per-member restriction of the self-hosted engine
// roles (#1215), layered under the super_admin's tenant gate (ADR 0084 decision 7).
//
// Every gate that asked tenantLimits.engineRoleAllowed now asks an engineRoleGate instead,
// and memberEngineGate answers "the tenant may AND this member may". The two layers are
// never OR'ed: the member grant can only narrow the tenant gate, so a super_admin's denial
// holds whatever a tenant_admin ticks.

import (
	"context"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineRoleGate is what a gate asks. tenantLimits satisfies it on its own (the tenant
// layer alone), memberEngineGate adds the member layer.
type engineRoleGate interface {
	engineRoleAllowed(api string) bool
}

// engineAccessRole maps the gateway's api name onto the vocabulary the tenant limits and
// the stored grants share. "" for an api no one can restrict per member (TTS).
func engineAccessRole(api string) string {
	switch api {
	case engineAPIChat:
		return store.EngineAccessLLM
	case engineAPIImages:
		return store.EngineAccessImage
	}
	return ""
}

// memberEngineGate is one member's view of both layers.
type memberEngineGate struct {
	lim          tenantLimits
	acc          store.EngineAccess
	membershipID string
}

func (g memberEngineGate) engineRoleAllowed(api string) bool {
	return g.lim.engineRoleAllowed(api) && g.memberAllowed(api)
}

func (g memberEngineGate) memberAllowed(api string) bool {
	role := engineAccessRole(api)
	return role == "" || g.acc.Allows(g.membershipID, role)
}

// forbidden is the refusal for a role this member may not use, or nil. Same code for both
// layers — the Agent treats either as "not offered" — but the message says which one, since
// the person who can change it differs (super_admin vs tenant_admin).
func (g memberEngineGate) forbidden(api string) *apiError {
	if !g.lim.engineRoleAllowed(api) {
		return engineForbiddenErr(api)
	}
	if !g.memberAllowed(api) {
		return &apiError{http.StatusForbidden, "engine_forbidden",
			"your tenant admin has not granted you the " + api + " engine"}
	}
	return nil
}

// engineGateFor is the request-time read for gates 1-3 and /props: uncached, like
// tenantLimitsFor, because each call authorizes exactly one request. The member layer is the
// narrowed read (one query, at most one row per role), since gate 3 runs on every relayed
// request and the whole tenant's grants would scale that with the member count.
func (g engineGateway) engineGateFor(ctx context.Context, mv store.MembershipView) (memberEngineGate, *apiError) {
	lim, aerr := g.tenantLimitsFor(ctx, mv.TenantID)
	if aerr != nil {
		return memberEngineGate{}, aerr
	}
	acc, err := g.mgr.store.GetMemberEngineAccess(ctx, mv.TenantID, mv.MembershipID)
	if err != nil {
		return memberEngineGate{}, internalErr(err)
	}
	return memberEngineGate{lim: lim, acc: acc, membershipID: mv.MembershipID}, nil
}

// memberEngineGateFor is gate 4's read: both layers from the per-tenant short-TTL cache
// (tenantEngineAccessFor), so the events tick still costs one read per tenant, not per tab.
func memberEngineGateFor(ctx context.Context, mgr *manager, mv store.MembershipView) memberEngineGate {
	lim, acc := tenantEngineAccessFor(ctx, mgr, mv.TenantID)
	return memberEngineGate{lim: lim, acc: acc, membershipID: mv.MembershipID}
}
