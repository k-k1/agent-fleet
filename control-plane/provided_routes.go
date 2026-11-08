package main

// provided_routes.go — wiring of the CP route gateway (ADR 0129 Q1, internal/routegateway) into
// the manager. It lives in package main because what it hands the gateway is the manager's: the
// store, the bridge epochs (bridge_epoch.go), the member connection registry
// (member_removal.go) and the super_admin gate with its audit row. The gateway itself is in
// internal/routegateway.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routegateway"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// activeProvidedRoutes is the gateway this CP started, for the shutdown drain. Nil when off.
var activeProvidedRoutes atomic.Pointer[routegateway.Gateway]

func providedRouteDeps(mgr *manager, assumer routegateway.Assumer, cfg routegateway.Config) routegateway.Deps {
	signKey := routegateway.SignKey(mgr.tokenSigningRoot())
	return routegateway.Deps{
		Store:       mgr.store,
		KeyAt:       func(epoch int64) []byte { return bridgeKeyAt(signKey, epoch) },
		BridgeEpoch: mgr.bridgeEpoch,
		VerifyIssuer: func(ctx context.Context, tok string) (string, int64, bool) {
			return mgr.verifyBridgeAt(ctx, signKey, tok, routegateway.VerifyIssueToken)
		},
		Track: func(ctx context.Context, mid string) (context.Context, func()) {
			return mgr.memberConns.track(ctx, mid)
		},
		Assumer: assumer,
		Config:  cfg,
	}
}

// registerProvidedRouteRoutes wires the gateway, its token route and the deployment source's
// admin routes when AF_PROVIDED_ROUTES=1. Off, nothing is registered: a 404 is what a deployment
// that never asked for provided routes answers.
func registerProvidedRouteRoutes(mux *http.ServeMux, cfg config) {
	if !routegateway.Enabled() {
		return
	}
	// One line either way: the log is how an operator confirms the switch took effect.
	if cfg.mgr == nil || cfg.mgr.store == nil {
		log.Printf("provided routes: %s=1 is set but this Control Plane has no store; the route gateway is not registered", routegateway.EnvSwitch)
		return
	}
	rc := routegateway.ConfigFromEnv()
	g := routegateway.New(providedRouteDeps(cfg.mgr, routegateway.NewSTSAssumer(), rc))
	providedRouteRoutes(mux, cfg.mgr, g)
	if activeProvidedRoutes.CompareAndSwap(nil, g) {
		log.Printf("provided routes: on (%s=1): the route gateway serves %s/ (in-flight cap %d per member, upstream deadline %s, drain %s); "+
			"the deployment source is registered with PUT /api/admin/provided-routes/deployment", routegateway.EnvSwitch, routegateway.Prefix,
			rc.MaxInFlight, rc.Deadline, rc.Drain)
		go g.Run(context.Background())
	}
}

func providedRouteRoutes(mux *http.ServeMux, mgr *manager, g *routegateway.Gateway) {
	// Session-exempt like /engine/: the caller is a Workspace with a bearer, not a browser.
	exemptPrefix(routegateway.Prefix+"/", "/internal/route/")
	mux.HandleFunc("POST /internal/route/token", g.Issue)
	mux.HandleFunc("POST /internal/route/check", g.Check)
	mux.HandleFunc(routegateway.Prefix+"/{path...}", g.Serve)
	a := providedRouteAdmin{memberAuth{mgr}, g}
	mux.HandleFunc("GET /api/admin/provided-routes/deployment", a.withSuperAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/provided-routes/deployment", a.withSuperAdmin(a.put))
	// Per tenant: the super_admin's grant and ceiling, the tenant_admin's policy and budgets
	// (ADR 0129 D3). tenant_admin routes are gated mid-handler like engine-access.
	mux.HandleFunc("GET /api/admin/tenants/{slug}/provided-routes/deployment", a.tenantView)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/provided-routes/deployment/grant", a.putGrant)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/provided-routes/deployment/policy", a.putPolicy)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/provided-routes/deployment/members", a.putMember)
	activeProvidedRouteGateway.Store(g)
}

// activeProvidedRouteGateway is the gateway the create paths ask (providedRouteCreateRefusal),
// set when its routes are registered.
var activeProvidedRouteGateway atomic.Pointer[routegateway.Gateway]

// drainProvidedRoutes is the shutdown hook: a no-op when the gateway is off.
func drainProvidedRoutes() {
	if g := activeProvidedRoutes.Load(); g != nil {
		g.Drain()
	}
}

// providedRouteIssueEnv is the Workspace's issuing credential for the CP route token, only while
// the switch is on, so a deployment that never asked carries no new credential. Its own family:
// a leak buys route tokens and nothing else, and those are worth what the member's grant and
// budget allow.
func providedRouteIssueEnv(root []byte, at func([]byte) []byte, membershipID string) []string {
	if !routegateway.Enabled() {
		return nil
	}
	// The claude bedrock-deployment row's acceptance gate (ADR 0128 decision 14) rides the same
	// switch: a Workspace started by a CP that serves the route is the one that may offer it.
	return []string{"AF_ROUTE_ISSUE_TOKEN=" + routegateway.MintIssueToken(at(routegateway.SignKey(root)), membershipID),
		"AF_CLAUDE_BEDROCK_DEPLOYMENT_ROUTE=1"}
}

// providedRouteAdmin is the super_admin's registration of the deployment source (ADR 0129 D1b;
// decided 2026-11-08: by REST with an audit row, the stack granting only the IAM).
type providedRouteAdmin struct {
	memberAuth
	g *routegateway.Gateway
}

type deploymentSourceAnswer struct {
	Source *routegateway.SourceView `json:"source"`
}

func (a providedRouteAdmin) get(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	src, ok, err := a.g.DeploymentSource(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var out deploymentSourceAnswer
	if ok {
		v := routegateway.ViewSource(src)
		out.Source = &v
	}
	writeJSON(w, http.StatusOK, out)
}

func (a providedRouteAdmin) put(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var in routegateway.SourceInput
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		msg := "invalid body: more than one JSON value"
		if err != nil {
			msg = "invalid body: " + err.Error()
		}
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", msg})
		return
	}
	src, err := a.g.PutDeploymentSource(r.Context(), in)
	var invalid *routegateway.InvalidInputError
	if errors.As(err, &invalid) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "invalid_source", err.Error()})
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(context.WithoutCancel(r.Context()), store.AuditLog{ID: store.NewID(), ActorKind: "user", ActorID: ident.ID,
		Action: "provided_route.deployment_source.put", Target: src.ID,
		Detail: fmt.Sprintf("role=%s region=%s models=%s fingerprint=%s policy_version=%d active=%t",
			src.RoleARN, strings.Join(src.Regions, ","), strings.Join(src.AllowList, ","), src.Fingerprint, src.PolicyVersion, src.Active),
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	v := routegateway.ViewSource(src)
	writeJSON(w, http.StatusOK, deploymentSourceAnswer{Source: &v})
}

// decodeAdminBody reads one JSON object with no unknown fields and nothing after it.
func decodeAdminBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		msg := "invalid body: more than one JSON value"
		if err != nil {
			msg = "invalid body: " + err.Error()
		}
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", msg})
		return false
	}
	return true
}

// writeRouteAdminErr answers an admin write's error: the input's own as 400, the store's as 500.
func writeRouteAdminErr(w http.ResponseWriter, err error) {
	var invalid *routegateway.InvalidInputError
	if errors.As(err, &invalid) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "invalid_input", err.Error()})
		return
	}
	writeAPIErr(w, internalErr(err))
}

func microDetail(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}

func (a providedRouteAdmin) tenantView(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	members, err := a.mgr.store.ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	v, err := a.g.View(r.Context(), t.ID, members)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putGrant is the super_admin's: a tenant_admin can narrow the grant, never give it.
func (a providedRouteAdmin) putGrant(w http.ResponseWriter, r *http.Request) {
	ident, ok := a.superAdminFor(w, r)
	if !ok {
		return
	}
	t, found, err := a.mgr.store.GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	var in routegateway.GrantInput
	if !decodeAdminBody(w, r, &in) {
		return
	}
	if err := a.g.SetGrant(r.Context(), t.ID, in); err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	a.audit(r, ident, t.ID, "provided_route.deployment_grant", t.Slug,
		fmt.Sprintf("granted=%t ceiling_micro=%s", in.Granted, microDetail(in.CeilingMicro)))
	writeJSON(w, http.StatusOK, in)
}

func (a providedRouteAdmin) putPolicy(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var in routegateway.PolicyInput
	if !decodeAdminBody(w, r, &in) {
		return
	}
	if err := a.g.SetPolicy(r.Context(), t.ID, in); err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	a.audit(r, ident, t.ID, "provided_route.deployment_policy", t.Slug,
		fmt.Sprintf("members_only=%t member_default_micro=%d", in.MembersOnly, in.MemberDefaultMicro))
	writeJSON(w, http.StatusOK, in)
}

func (a providedRouteAdmin) putMember(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var in routegateway.MemberInput
	if !decodeAdminBody(w, r, &in) {
		return
	}
	// The membership id comes from the client: it is checked against THIS tenant's roster, so
	// no tenant_admin writes a budget onto another tenant's seat.
	members, err := a.mgr.store.ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var target *store.MemberInfo
	for i := range members {
		if members[i].MembershipID == in.MembershipID {
			target = &members[i]
			break
		}
	}
	if target == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_member", "no such member in this tenant"})
		return
	}
	if err := a.g.SetMember(r.Context(), t.ID, in); err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	a.audit(r, ident, t.ID, "provided_route.deployment_member", target.UserKey,
		fmt.Sprintf("granted=%t budget_micro=%s", in.Granted, microDetail(in.BudgetMicro)))
	writeJSON(w, http.StatusOK, in)
}

func (a providedRouteAdmin) audit(r *http.Request, ident store.Identity, tenantID, action, target, detail string) {
	_ = a.mgr.store.InsertAudit(context.WithoutCancel(r.Context()), store.AuditLog{ID: store.NewID(), TenantID: tenantID,
		ActorKind: "user", ActorID: ident.ID, Action: action, Target: target, Detail: detail,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
}

// providedRouteCreateRefusal is ADR 0129 D3's "before waking the Workspace" for every create
// path (REST and the Console, MCP create_session, a schedule's create, update and fire): a
// selection naming a provided route the member cannot use now is refused here, as an unknown
// route is. nil for every other route. The Agent's bind, the token endpoint and every call check
// again; this only keeps a doomed create from waking a stopped Workspace.
func providedRouteCreateRefusal(ctx context.Context, membershipID, route string) *apiError {
	route = strings.TrimSpace(route)
	if providedroutes.SourceOf(route) == "" {
		return nil
	}
	g := activeProvidedRouteGateway.Load()
	if g == nil {
		return &apiError{http.StatusConflict, agentroutes.CodeRouteUnavailable,
			"route " + route + " is not offered by this deployment (its provided routes are off)"}
	}
	if _, ref := g.Usable(ctx, membershipID, route); ref != nil {
		status := ref.Status
		if status == http.StatusUnauthorized {
			status = http.StatusForbidden
		}
		return &apiError{status, ref.Code, ref.Message}
	}
	return nil
}
