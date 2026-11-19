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
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routegateway"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicysrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
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
	// Even a failed PUT can have stored the registration before the activation step failed.
	invalidateAllProvidedRevisions()
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
	warn, err := a.g.SetGrant(r.Context(), t.ID, in)
	if err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	invalidateProvidedRevision(t.ID)
	a.audit(r, ident, t.ID, "provided_route.deployment_grant", t.Slug,
		fmt.Sprintf("granted=%t ceiling_micro=%s", in.Granted, microDetail(in.CeilingMicro)))
	writeJSON(w, http.StatusOK, grantSaved{in, nonNil(warn)})
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
	warn, err := a.g.SetPolicy(r.Context(), t.ID, in)
	if err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	invalidateProvidedRevision(t.ID)
	a.audit(r, ident, t.ID, "provided_route.deployment_policy", t.Slug,
		fmt.Sprintf("members_only=%t member_default_micro=%d", in.MembersOnly, in.MemberDefaultMicro))
	writeJSON(w, http.StatusOK, policySaved{in, nonNil(warn)})
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
	warn, err := a.g.SetMember(r.Context(), t.ID, in)
	if err != nil {
		writeRouteAdminErr(w, err)
		return
	}
	invalidateProvidedRevision(t.ID)
	a.audit(r, ident, t.ID, "provided_route.deployment_member", target.UserKey,
		fmt.Sprintf("granted=%t budget_micro=%s", in.Granted, microDetail(in.BudgetMicro)))
	writeJSON(w, http.StatusOK, memberSaved{in, nonNil(warn)})
}

// grantSaved, policySaved and memberSaved echo a saved write with its warnings: amounts a call
// fits under only with a small output cap (routegateway's belowFloor). warnings is never null.
type grantSaved struct {
	routegateway.GrantInput
	Warnings []string `json:"warnings"`
}

type policySaved struct {
	routegateway.PolicyInput
	Warnings []string `json:"warnings"`
}

type memberSaved struct {
	routegateway.MemberInput
	Warnings []string `json:"warnings"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

// routePolicyCreateRefusal is the credential policy's pre-wake refusal of a create's explicit
// route (ADR 0138 D12): a session create, a schedule's save and fire. nil for an empty selection,
// which only the Agent can resolve (D11), and while the tenant's policy restricts nothing.
func routePolicyCreateRefusal(ctx context.Context, st store.Store, tenantID, route string) *apiError {
	if e := routepolicysrv.RouteRefusal(ctx, st, tenantID, route); e != nil {
		return &apiError{e.Status, e.Code, e.Message}
	}
	return nil
}

// agentRoutePolicy reads the "route_policy" capability of a running Agent from its /healthz
// (routepolicysrv.AgentEnforceCapability). answered is false while it does not answer 200. A seam
// for tests.
var agentRoutePolicy = func(ctx context.Context, rt runtime.Runtime) (reported string, answered bool) {
	if rt == nil || rt.Endpoint() == "" {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/healthz", nil)
	if err != nil {
		return "", false
	}
	resp, err := homeKeyHealthzClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var r struct {
		RoutePolicy string `json:"route_policy"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&r) != nil {
		return "", false
	}
	return r.RoutePolicy, true
}

// agentPolicyRefusal is D12's Upgrade rule, asked once the Workspace is running and before a
// create is handed to it: a restricted member's create is refused when the Agent does not report
// that it enforces the policy. A Workspace woken for the create runs the current image; one that
// was already running may still run an Agent from before enforcement. nil for an unrestricted
// member, or a kind that runs no model, without asking the Agent anything.
func agentPolicyRefusal(ctx context.Context, st store.Store, tenantID, kind string, rt runtime.Runtime) *apiError {
	if agentroutes.RunsNoModel(kind) || !routepolicysrv.Restricted(ctx, st, tenantID) {
		return nil
	}
	reported, answered := agentRoutePolicy(ctx, rt)
	if e := routepolicysrv.AgentRefusal(reported, answered); e != nil {
		return &apiError{e.Status, e.Code, e.Message}
	}
	return nil
}

// --- the events stream's provided_routes frame ---

// The events stream's "provided_routes" frame (ADR 0129 D3, the
// fourth place: the Console is told when a grant or a budget changes).
//
// The events stream is poll-and-diff, so there is no publish call: each tick reads one opaque
// revision of the subscriber's own tenant and a frame goes out only when it changed. That also
// reaches a subscriber on another CP replica, which an in-process publish from the PUT could
// not. The frame is `{"rev":"<digest>"}` and nothing else: no amounts, ids or names, and it is
// built from the subscriber's own tenant alone, so it cannot name another tenant's change; only
// that tenant's admins (and super_admins) are sent it.

// providedRevisionTTL bounds the per-tenant read the way tenantEngineLimitsTTL does: one query
// pair per tenant per interval, not per open tab per tick. A save on this replica drops the
// entry, so only a save on another replica waits it out.
const providedRevisionTTL = 3 * time.Second

type providedRevisionEntry struct {
	mu  sync.Mutex
	at  time.Time
	rev string
}

var providedRevisionCache sync.Map // tenantID -> *providedRevisionEntry

// providedAdminTTL bounds the role check the same way: the frame is a signal about values only
// tenant admins may read (GET .../provided-routes/deployment is tenant_admin or super_admin), so
// a plain member must get none, not even its timing.
const providedAdminTTL = 3 * time.Second

type providedAdminEntry struct {
	at time.Time
	ok bool
}

var providedAdminCache sync.Map // identity id + "/" + tenant id -> providedAdminEntry

func providedAdminFor(ctx context.Context, mgr *manager, ident store.Identity, tenantID string) bool {
	key := ident.ID + "/" + tenantID
	if v, ok := providedAdminCache.Load(key); ok {
		if e := v.(providedAdminEntry); time.Since(e.at) < providedAdminTTL {
			return e.ok
		}
	}
	ok := mgr.tenantAdminFor(ctx, ident, tenantID)
	providedAdminCache.Store(key, providedAdminEntry{at: time.Now(), ok: ok})
	return ok
}

// providedRoutesPayload is the stream's frame for the subscriber's own tenant. ok is false when
// the subscriber may not read the tenant's provided routes, has no tenant, or the revision cannot
// be read (a store error drops this tick only).
func providedRoutesPayload(ctx context.Context, mgr *manager, ident store.Identity, tenantID string) (map[string]any, bool) {
	if tenantID == "" || mgr == nil || mgr.store == nil || !providedAdminFor(ctx, mgr, ident, tenantID) {
		return nil, false
	}
	v, _ := providedRevisionCache.LoadOrStore(tenantID, &providedRevisionEntry{})
	e := v.(*providedRevisionEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rev == "" || time.Since(e.at) >= providedRevisionTTL {
		rev, err := mgr.store.ProvidedRouteRevision(ctx, tenantID, providedroutes.SourceDeployment)
		if err != nil {
			return nil, false
		}
		e.rev, e.at = rev, time.Now()
	}
	return map[string]any{"rev": e.rev}, true
}

// invalidateProvidedRevision drops one tenant's cached revision, and the super_admin's, after a
// committed write, so the next tick reads the stored values instead of waiting out
// providedRevisionTTL.
func invalidateProvidedRevision(tenantID string) {
	providedRevisionCache.Delete(tenantID)
	dropProvidedAdminRevs()
}

// invalidateAllProvidedRevisions is for a write every tenant's view depends on: the deployment
// source (registered, on, its model list).
func invalidateAllProvidedRevisions() {
	providedRevisionCache.Range(func(k, _ any) bool { providedRevisionCache.Delete(k); return true })
	dropProvidedAdminRevs()
}

func dropProvidedAdminRevs() {
	providedAdminRevCache.Range(func(k, _ any) bool { providedAdminRevCache.Delete(k); return true })
}

// The super_admin's frame, "provided_routes_admin" (ADR 0129 D3 gate 4, #4371): one opaque digest
// over every tenant's grant, policy and member overrides and the deployment source, so a page of
// another tenant, the tenant table's grant cells and the source editor read again when any of it
// moves. `{"rev":"<digest>"}` and nothing else, sent to super_admins only: they can read all of
// it through the REST views, and nobody else may learn even when it changed.
//
// The cache is keyed by the store, so two stores in one process (the tests) never share a digest.
var providedAdminRevCache sync.Map // store.Store -> *providedRevisionEntry

// providedSuperAdminTTL bounds the role re-check. The stream's identity was resolved when it
// opened, so a demotion (another replica's DemoteSuperAdmins, a DB edit) would otherwise leave the
// frame flowing; unknown or an error answers false.
const providedSuperAdminTTL = 3 * time.Second

var providedSuperAdminCache sync.Map // identity id -> providedAdminEntry

func providedSuperAdminNow(ctx context.Context, mgr *manager, ident store.Identity) bool {
	if ident.ID == "" || ident.Role != "super_admin" {
		return false
	}
	if v, ok := providedSuperAdminCache.Load(ident.ID); ok {
		if e := v.(providedAdminEntry); time.Since(e.at) < providedSuperAdminTTL {
			return e.ok
		}
	}
	cur, found, err := mgr.store.GetIdentityByID(ctx, ident.ID)
	ok := err == nil && found && cur.Role == "super_admin"
	providedSuperAdminCache.Store(ident.ID, providedAdminEntry{at: time.Now(), ok: ok})
	return ok
}

func providedRoutesAdminPayload(ctx context.Context, mgr *manager, ident store.Identity) (map[string]any, bool) {
	if mgr == nil || mgr.store == nil || !providedSuperAdminNow(ctx, mgr, ident) {
		return nil, false
	}
	v, _ := providedAdminRevCache.LoadOrStore(mgr.store, &providedRevisionEntry{})
	e := v.(*providedRevisionEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rev == "" || time.Since(e.at) >= providedRevisionTTL {
		rev, err := mgr.store.ProvidedRouteAdminRevision(ctx, providedroutes.SourceDeployment)
		if err != nil {
			return nil, false
		}
		e.rev, e.at = rev, time.Now()
	}
	return map[string]any{"rev": e.rev}, true
}
