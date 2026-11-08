package routegateway

// The check a create path and the Agent run before a session is started on a provided route
// (ADR 0129 D3: "before waking the Workspace", and the four places of ADR 0084 decision 8). It
// is the per-call check of gateway.go without a call: the membership is live, the tenant and the
// member are granted, the deployment source exists and is active, and the member has budget left
// at both levels. What it answers is advice for the moment; the token endpoint and every call
// check again.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// UsableRoute is what a member may use now: the binding a selection is stored with, the Region
// the calls go to, the models the source allows, and the smaller of what is left of the member's
// budget and of the tenant's ceiling this month.
type UsableRoute struct {
	Route          string   `json:"route"`
	Binding        string   `json:"binding"`
	Region         string   `json:"region"`
	Models         []string `json:"models"`
	RemainingMicro int64    `json:"remaining_micro"`
	Reset          string   `json:"reset"`
}

// UseRefusal is why a member may not use a provided route now. Status is the meaning (403 not
// granted, 409 re-pointed, 503 budget used up or the store unreadable), not the harness wire
// status the gateway maps its refusals to.
type UseRefusal struct {
	Status  int
	Code    string
	Message string
}

func (r *UseRefusal) Error() string { return r.Message }

// Usable is the check for one membership and one route id.
func (g *Gateway) Usable(ctx context.Context, membershipID, route string) (UsableRoute, *UseRefusal) {
	mv, ref := g.liveMembership(ctx, membershipID)
	if ref != nil {
		return UsableRoute{}, ref.use()
	}
	return g.usableFor(ctx, mv, route)
}

func (g *Gateway) usableFor(ctx context.Context, mv store.MembershipView, route string) (UsableRoute, *UseRefusal) {
	if providedroutes.SourceOf(route) != providedroutes.SourceDeployment {
		return UsableRoute{}, &UseRefusal{http.StatusBadRequest, "route_unsupported", "this Control Plane serves the deployment's Bedrock route only"}
	}
	src, ok, err := g.DeploymentSource(ctx)
	if err != nil {
		return UsableRoute{}, &UseRefusal{http.StatusServiceUnavailable, "route_unavailable", "the route could not be read"}
	}
	if !ok {
		return UsableRoute{}, &UseRefusal{http.StatusForbidden, "route_source_missing",
			"this deployment has no Bedrock route of its own registered (a super_admin registers it)"}
	}
	use, ref := g.authorizeUse(ctx, mv, route, src.Binding())
	if ref != nil {
		return UsableRoute{}, ref.use()
	}
	now := g.now().UTC()
	month := providedroutes.AdmissionMonth(now)
	reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	mp, err := g.d.Store.GetProvidedRoutePeriod(ctx, mv.TenantID, mv.MembershipID, providedroutes.SourceDeployment, month)
	if err != nil {
		return UsableRoute{}, &UseRefusal{http.StatusServiceUnavailable, "route_unavailable", "the route's spend could not be read"}
	}
	tp, err := g.d.Store.GetProvidedRoutePeriod(ctx, mv.TenantID, "", providedroutes.SourceDeployment, month)
	if err != nil {
		return UsableRoute{}, &UseRefusal{http.StatusServiceUnavailable, "route_unavailable", "the route's spend could not be read"}
	}
	left := use.lim.MemberBudgetMicro - mp.SpentMicro - mp.ReservedMicro
	if t := *use.lim.TenantCeilingMicro - tp.SpentMicro - tp.ReservedMicro; t < left {
		if t <= 0 {
			return UsableRoute{}, g.budgetRefusal(ctx, use, store.ProvidedRouteRefusedTenantCeiling, 0).use()
		}
		left = t
	}
	if left <= 0 {
		return UsableRoute{}, g.budgetRefusal(ctx, use, store.ProvidedRouteRefusedMemberBudget, 0).use()
	}
	return UsableRoute{Route: route, Binding: src.Binding(), Region: use.region, Models: append([]string{}, src.AllowList...),
		RemainingMicro: left, Reset: reset.Format(time.RFC3339)}, nil
}

func (r *routeRefusal) use() *UseRefusal {
	return &UseRefusal{Status: r.status, Code: r.code, Message: r.msg}
}

// Check is POST /internal/route/check {route}, on the Agent's issuing token: what the Agent binds
// a selection to and whether it offers the route. Refusals keep their meaning's status (the
// Agent shows them to whoever is creating the session, not to a harness).
func (g *Gateway) Check(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mid, _, ok := g.d.VerifyIssuer(ctx, routeBearer(r))
	if !ok {
		writeUseRefusal(w, &UseRefusal{http.StatusUnauthorized, "route_token_invalid", "invalid route issuing token"})
		return
	}
	var req struct {
		Route string `json:"route"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<12))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeUseRefusal(w, &UseRefusal{http.StatusBadRequest, "route_bad_request", "invalid body"})
		return
	}
	u, ref := g.Usable(ctx, mid, strings.TrimSpace(req.Route))
	if ref != nil {
		writeUseRefusal(w, ref)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// useRefusalWire is a refusal's body, in the shape the gateway's refusals have.
type useRefusalWire struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func writeUseRefusal(w http.ResponseWriter, r *UseRefusal) {
	writeJSON(w, r.Status, useRefusalWire{Message: r.Message, Code: r.Code})
}
