package main

// route_policy.go — the credential policy's administration (ADR 0138 D7–D9, D15): the operator
// ceiling read from the environment at start, and the audited REST of the three administered
// layers. Nothing here enforces the policy; the Agent's pull and the enforcement points come in
// the phases after this one, so with every layer empty, which is the default, no behaviour
// changes.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ceilingState is the operator ceiling this CP started with.
type ceilingState struct {
	raw  string
	rule routepolicy.Rule
}

var routePolicyCeiling atomic.Pointer[ceilingState]

// loadRoutePolicyCeiling reads the ceiling once at start. An unparsable or unknown value refuses
// the start rather than reading as "allow all" (D9c).
func loadRoutePolicyCeiling() error {
	raw, rule, err := routepolicy.CeilingFromEnv()
	if err != nil {
		return err
	}
	routePolicyCeiling.Store(&ceilingState{raw: raw, rule: rule})
	if rule.Restricted() {
		log.Printf("route policy: operator ceiling %s=%q", routepolicy.EnvCeiling, raw)
	}
	return nil
}

func currentCeiling() ceilingState {
	if c := routePolicyCeiling.Load(); c != nil {
		return *c
	}
	return ceilingState{}
}

func registerRoutePolicyRoutes(mux *http.ServeMux, cfg config) {
	a := routePolicyAdmin{memberAuth{cfg.mgr}}
	mux.HandleFunc("GET /api/admin/route-policy", a.withSuperAdmin(a.getDeployment))
	mux.HandleFunc("PUT /api/admin/route-policy", a.withSuperAdmin(a.putDeployment))
	mux.HandleFunc("GET /api/admin/tenants/{slug}/route-policy", a.getTenant)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/route-policy", a.putTenant)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/route-policy/cap", a.putCap)
}

type routePolicyAdmin struct{ memberAuth }

// policyLayerWire is one stored layer as the API shows it.
type policyLayerWire struct {
	Rule      routepolicy.Rule `json:"rule"`
	Revision  int64            `json:"revision"`
	UpdatedAt string           `json:"updatedAt,omitempty"`
}

// ceilingWire is the operator ceiling, read-only, with its source (D9c, D16).
type ceilingWire struct {
	Raw    string           `json:"raw"`
	Rule   routepolicy.Rule `json:"rule"`
	Source string           `json:"source"`
}

func ceilingView() ceilingWire {
	c := currentCeiling()
	return ceilingWire{Raw: c.raw, Rule: c.rule, Source: "deployment"}
}

// routeDecisionWire is one catalogued route under the effective policy, for an administrator:
// what it is, whether it is allowed, and which layers forbid it (D10a: the admin pages list what
// is hidden and why; members never see this).
type routeDecisionWire struct {
	ID          string   `json:"id"`
	Class       string   `json:"class"`
	Destination string   `json:"destination"`
	Allowed     bool     `json:"allowed"`
	ForbiddenBy []string `json:"forbiddenBy,omitempty"`
}

type tenantPolicyWire struct {
	Ceiling    ceilingWire         `json:"ceiling"`
	Deployment policyLayerWire     `json:"deployment"`
	Cap        policyLayerWire     `json:"cap"`
	Tenant     policyLayerWire     `json:"tenant"`
	Restricted bool                `json:"restricted"`
	Revision   int64               `json:"revision"`
	Routes     []routeDecisionWire `json:"routes"`
}

func layerOf(row store.RoutePolicyRow) (policyLayerWire, error) {
	var r routepolicy.Rule
	if err := json.Unmarshal([]byte(row.Rule), &r); err != nil {
		return policyLayerWire{}, fmt.Errorf("route policy %s/%s: stored rule does not parse: %w", row.Scope, row.TenantID, err)
	}
	return policyLayerWire{Rule: r, Revision: row.Revision, UpdatedAt: row.UpdatedAt}, nil
}

// tenantPolicy reads every layer a tenant's members are judged by.
func tenantPolicy(ctx context.Context, st store.Store, tenantID string) (tenantPolicyWire, error) {
	out := tenantPolicyWire{Ceiling: ceilingView()}
	for _, l := range []struct {
		scope, tenant string
		into          *policyLayerWire
	}{{store.RoutePolicyDeployment, "", &out.Deployment}, {store.RoutePolicyTenantCap, tenantID, &out.Cap},
		{store.RoutePolicyTenant, tenantID, &out.Tenant}} {
		row, err := st.GetRoutePolicy(ctx, l.scope, l.tenant)
		if err != nil {
			return out, err
		}
		if *l.into, err = layerOf(row); err != nil {
			return out, err
		}
		out.Revision = max(out.Revision, row.Revision)
	}
	layers := routepolicy.Layers{Ceiling: out.Ceiling.Rule, Deployment: out.Deployment.Rule, TenantCap: out.Cap.Rule, Tenant: out.Tenant.Rule}
	out.Restricted = layers.Restricted()
	out.Routes = []routeDecisionWire{}
	for _, rt := range agentroutes.Routes() {
		by := layers.ForbiddenBy(rt.ID)
		out.Routes = append(out.Routes, routeDecisionWire{ID: rt.ID, Class: rt.Class, Destination: rt.Destination,
			Allowed: len(by) == 0, ForbiddenBy: by})
	}
	return out, nil
}

func (a routePolicyAdmin) getDeployment(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	row, err := a.mgr.store.GetRoutePolicy(r.Context(), store.RoutePolicyDeployment, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	l, err := layerOf(row)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Ceiling    ceilingWire     `json:"ceiling"`
		Deployment policyLayerWire `json:"deployment"`
	}{ceilingView(), l})
}

// readRule decodes and validates a rule body. above, when set, is the layer the rule may not
// widen (the operator ceiling for a super_admin's write, §8 M1).
func readRule(w http.ResponseWriter, r *http.Request, above *routepolicy.Rule) (routepolicy.Rule, string, bool) {
	var in routepolicy.Rule
	if !decodeAdminBody(w, r, &in) {
		return in, "", false
	}
	if err := in.Validate(); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "invalid_route_policy", err.Error()})
		return in, "", false
	}
	in = in.Normalize()
	if above != nil {
		if wider := in.Exceeds(*above); len(wider) > 0 {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "route_policy_exceeds_ceiling",
				fmt.Sprintf("the deployment's ceiling forbids routes this rule would allow: %v; a layer below the ceiling can only narrow it", wider)})
			return in, "", false
		}
	}
	b, err := json.Marshal(in)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return in, "", false
	}
	return in, string(b), true
}

// putLayer writes one layer and its audit row (D15: before and after).
func (a routePolicyAdmin) putLayer(w http.ResponseWriter, r *http.Request, ident store.Identity, scope string, t store.Tenant, rule string) {
	before, after, err := a.mgr.store.PutRoutePolicy(r.Context(), scope, t.ID, rule, ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	target := t.Slug
	if scope == store.RoutePolicyDeployment {
		target = "deployment"
	}
	_ = a.mgr.store.InsertAudit(context.WithoutCancel(r.Context()), store.AuditLog{ID: store.NewID(), TenantID: t.ID,
		ActorKind: "user", ActorID: ident.ID, Action: "route_policy." + scope + ".put", Target: target,
		Detail:     fmt.Sprintf("before=%s after=%s revision=%d", before.Rule, after.Rule, after.Revision),
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	l, err := layerOf(after)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a routePolicyAdmin) putDeployment(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	c := currentCeiling()
	if _, rule, ok := readRule(w, r, &c.rule); ok {
		a.putLayer(w, r, ident, store.RoutePolicyDeployment, store.Tenant{}, rule)
	}
}

// putCap is the super_admin's cap on one tenant; a tenant_admin can neither write nor clear it.
func (a routePolicyAdmin) putCap(w http.ResponseWriter, r *http.Request) {
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
	c := currentCeiling()
	if _, rule, ok := readRule(w, r, &c.rule); ok {
		a.putLayer(w, r, ident, store.RoutePolicyTenantCap, t, rule)
	}
}

// putTenant is the tenant_admin's own rule. It may name exceptions the layers above do not have:
// those reopen nothing (D1d, D7a), so they are stored as written rather than refused.
func (a routePolicyAdmin) putTenant(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if _, rule, ok := readRule(w, r, nil); ok {
		a.putLayer(w, r, ident, store.RoutePolicyTenant, t, rule)
	}
}

func (a routePolicyAdmin) getTenant(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	v, err := tenantPolicy(r.Context(), a.mgr.store, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}
