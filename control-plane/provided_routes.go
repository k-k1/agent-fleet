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
	"net/http"
	"strings"
	"sync/atomic"

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
	if !routegateway.Enabled() || cfg.mgr == nil || cfg.mgr.store == nil {
		return
	}
	g := routegateway.New(providedRouteDeps(cfg.mgr, routegateway.NewSTSAssumer(), routegateway.ConfigFromEnv()))
	providedRouteRoutes(mux, cfg.mgr, g)
	if activeProvidedRoutes.CompareAndSwap(nil, g) {
		go g.Run(context.Background())
	}
}

func providedRouteRoutes(mux *http.ServeMux, mgr *manager, g *routegateway.Gateway) {
	// Session-exempt like /engine/: the caller is a Workspace with a bearer, not a browser.
	exemptPrefix(routegateway.Prefix+"/", "/internal/route/")
	mux.HandleFunc("POST /internal/route/token", g.Issue)
	mux.HandleFunc(routegateway.Prefix+"/{path...}", g.Serve)
	a := providedRouteAdmin{memberAuth{mgr}, g}
	mux.HandleFunc("GET /api/admin/provided-routes/deployment", a.withSuperAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/provided-routes/deployment", a.withSuperAdmin(a.put))
}

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
	return []string{"AF_ROUTE_ISSUE_TOKEN=" + routegateway.MintIssueToken(at(routegateway.SignKey(root)), membershipID)}
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
	var invalid *routegateway.InvalidSourceError
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
