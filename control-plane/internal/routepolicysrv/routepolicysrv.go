// Package routepolicysrv is the credential policy's administration (ADR 0138 D7–D9, D15): the
// operator ceiling read from the environment at start and recorded under a revision, and the
// audited REST of the three administered layers. Nothing here enforces the policy; the Agent's
// pull and the enforcement points come in later phases, so with every layer empty, which is the
// default, no behaviour changes.
package routepolicysrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Error codes this package emits. This package cannot import package main, so the same values
// are declared as errCode* in errcodes.go (which puts them in the Console-catalogue test's view);
// TestRoutePolicyErrCodesMatchRootConstants pins each pair. Keep the spellings identical in
// errcodes.go, here, and console/src/lib/i18n/locales/{en,ja}/errors.ts. Package-local codes
// like mcpsrv's, plus the root registration.
const (
	codeInvalidRoutePolicy               = "invalid_route_policy"
	codeRoutePolicyExceedsCeiling        = "route_policy_exceeds_ceiling"
	codeRoutePolicyEndpointNotApprovable = "route_policy_endpoint_not_approvable"
	codeRoutePolicyStale                 = "route_policy_stale"
)

// ceilingState is the operator ceiling this CP started with.
type ceilingState struct {
	raw  string
	rule routepolicy.Rule
}

var routePolicyCeiling atomic.Pointer[ceilingState]

// LoadCeiling reads the ceiling once at start. An unparsable or unknown value refuses
// the start rather than reading as "allow all" (D9c).
func LoadCeiling() error {
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

// SyncCeiling records this CP's ceiling as the stored ceiling row when it differs from
// what is stored, under a new revision and with an audit row, so the change takes part in every
// member's effective revision; an unchanged ceiling writes nothing and moves no revision.
func SyncCeiling(ctx context.Context, st store.Store) error {
	c := currentCeiling()
	want, err := json.Marshal(c.rule)
	if err != nil {
		return err
	}
	row, err := st.GetRoutePolicy(ctx, store.RoutePolicyCeiling, "")
	if err != nil {
		return err
	}
	if row.Rule == string(want) || row.Revision == 0 && !c.rule.Restricted() {
		return nil
	}
	_, after, err := st.PutRoutePolicy(ctx, store.RoutePolicyCeiling, "", string(want), "operator",
		func(before, after store.RoutePolicyRow) store.AuditLog {
			return store.AuditLog{ID: store.NewID(), ActorKind: "system", ActorID: "operator", Action: "route_policy.ceiling.sync",
				Target: "deployment", Detail: fmt.Sprintf("before=%s after=%s revision=%d raw=%q", before.Rule, after.Rule, after.Revision, c.raw),
				HTTPStatus: http.StatusOK, At: store.NowTS()}
		})
	if err != nil {
		return fmt.Errorf("recording the operator ceiling: %w", err)
	}
	log.Printf("route policy: the operator ceiling changed, recorded at revision %d", after.Revision)
	return nil
}

func currentCeiling() ceilingState {
	if c := routePolicyCeiling.Load(); c != nil {
		return *c
	}
	return ceilingState{}
}

func Register(mux *http.ServeMux, cp CP) {
	a := admin{cp}
	mux.HandleFunc("GET /api/admin/route-policy", a.withSuperAdmin(a.getDeployment))
	mux.HandleFunc("PUT /api/admin/route-policy", a.withSuperAdmin(a.putDeployment))
	mux.HandleFunc("GET /api/admin/route-policy/tenant-counts", a.withSuperAdmin(a.getTenantCounts))
	mux.HandleFunc("GET /api/admin/tenants/{slug}/route-policy", a.getTenant)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/route-policy", a.putTenant)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/route-policy/cap", a.putCap)
	mux.HandleFunc("GET "+BridgePath, a.pull)
}

// admin serves the policy REST over the CP seam.
type admin struct{ cp CP }

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

// endpointDecisionWire is one host the tenant rule approves, for an administrator: whether it is
// approved in effect, and what keeps it from being (a layer above, or "vendor").
type endpointDecisionWire struct {
	Host       string   `json:"host"`
	Approved   bool     `json:"approved"`
	ExcludedBy []string `json:"excludedBy,omitempty"`
}

type tenantPolicyWire struct {
	Ceiling    ceilingWire         `json:"ceiling"`
	Deployment policyLayerWire     `json:"deployment"`
	Cap        policyLayerWire     `json:"cap"`
	Tenant     policyLayerWire     `json:"tenant"`
	Restricted bool                `json:"restricted"`
	Revision   int64               `json:"revision"`
	Routes     []routeDecisionWire `json:"routes"`
	// Endpoints is the tenant rule's hosts and their effect (ADR 0138 §2); EndpointRoutes the
	// per-connection routes every layer allows on an approved one.
	Endpoints      []endpointDecisionWire `json:"endpoints"`
	EndpointRoutes []string               `json:"endpointRoutes"`
	// Hidden is what the policy hides from the tenant's members, and which layers hide it (D10a).
	Hidden hiddenWire `json:"hidden"`
	// Owners is every harness, chat backend and image provider with its routes and the ones the
	// layers allow; additive to Hidden.
	Owners ownersWire `json:"owners"`
	// Vocabulary is what a rule may name; the same value the deployment GET carries.
	Vocabulary vocabularyWire `json:"vocabulary"`
	layers     routepolicy.Layers
}

func layerOf(row store.RoutePolicyRow) (policyLayerWire, error) {
	var r routepolicy.Rule
	if err := json.Unmarshal([]byte(row.Rule), &r); err != nil {
		return policyLayerWire{}, fmt.Errorf("route policy %s/%s: stored rule does not parse: %w", row.Scope, row.TenantID, err)
	}
	return policyLayerWire{Rule: r, Revision: row.Revision, UpdatedAt: row.UpdatedAt}, nil
}

// errCeilingUnsettled: the stored ceiling kept differing from this CP's own after re-recording
// it, which happens only while Control Planes with different ceilings write it in turn. Nothing
// is served rather than an answer whose revision another CP labels with other content.
var errCeilingUnsettled = errors.New("route policy: the recorded operator ceiling keeps changing under this Control Plane (Control Planes with different ceilings?)")

// tenantPolicy reads every layer a tenant's members are judged by, in one snapshot, and labels it
// with that snapshot's revision. The ceiling evaluated is always this CP's own, from its
// environment (D9c: it holds even if the database is edited). A stored ceiling that differs from
// it (a database edit, or another CP with another ceiling) is re-recorded first, under a new
// revision, and the layers read again, so the answer's revision is the one its ceiling was
// recorded at and never a revision another reading labels with a looser rule.
func TenantPolicy(ctx context.Context, st store.Store, tenantID string) (tenantPolicyWire, error) {
	c := currentCeiling()
	want, err := json.Marshal(c.rule)
	if err != nil {
		return tenantPolicyWire{}, err
	}
	var rows store.RoutePolicyLayers
	for attempt := 0; ; attempt++ {
		if rows, err = st.GetRoutePolicyLayers(ctx, tenantID); err != nil {
			return tenantPolicyWire{}, err
		}
		if rows.Ceiling.Rule == string(want) || rows.Ceiling.Revision == 0 && !c.rule.Restricted() {
			break
		}
		if attempt == 3 {
			return tenantPolicyWire{}, errCeilingUnsettled
		}
		if err := SyncCeiling(ctx, st); err != nil {
			return tenantPolicyWire{}, err
		}
	}
	out := tenantPolicyWire{Ceiling: ceilingView(), Revision: rows.Revision(), Vocabulary: vocabulary()}
	for _, l := range []struct {
		row  store.RoutePolicyRow
		into *policyLayerWire
	}{{rows.Deployment, &out.Deployment}, {rows.TenantCap, &out.Cap}, {rows.Tenant, &out.Tenant}} {
		if *l.into, err = layerOf(l.row); err != nil {
			return out, err
		}
	}
	layers := routepolicy.Layers{Ceiling: c.rule, Deployment: out.Deployment.Rule, TenantCap: out.Cap.Rule, Tenant: out.Tenant.Rule}
	out.layers = layers
	out.Restricted = layers.Restricted()
	out.Endpoints = []endpointDecisionWire{}
	if hosts := out.Tenant.Rule.Endpoints; hosts != nil {
		for _, h := range *hosts {
			by := layers.EndpointExcludedBy(h)
			out.Endpoints = append(out.Endpoints, endpointDecisionWire{Host: h, Approved: len(by) == 0, ExcludedBy: by})
		}
	}
	out.EndpointRoutes = append([]string{}, layers.EndpointRoutes()...)
	out.Routes = []routeDecisionWire{}
	for _, rt := range agentroutes.Routes() {
		by := layers.ForbiddenBy(rt.ID)
		out.Routes = append(out.Routes, routeDecisionWire{ID: rt.ID, Class: rt.Class, Destination: rt.Destination,
			Allowed: len(by) == 0, ForbiddenBy: by})
	}
	approved := 0
	for _, e := range out.Endpoints {
		if e.Approved {
			approved++
		}
	}
	out.Owners, out.Hidden = foldOwners(layers, out.EndpointRoutes, approved)
	return out, nil
}

func (a admin) getDeployment(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	v, err := TenantPolicy(r.Context(), a.cp.Store(), "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Ceiling    ceilingWire     `json:"ceiling"`
		Deployment policyLayerWire `json:"deployment"`
		Revision   int64           `json:"revision"`
		Vocabulary vocabularyWire  `json:"vocabulary"`
	}{v.Ceiling, v.Deployment, v.Revision, v.Vocabulary})
}

// tenantCountsWire is the admin Overview's count of tenants by what restricts them. Counts only:
// no tenant is named and no rule is shown, so the answer holds nothing a tenant keeps private.
type tenantCountsWire struct {
	// Tenants is every tenant of the deployment.
	Tenants int `json:"tenants"`
	// WithCap counts tenants whose cap (the super_admin's layer) narrows anything, endpoint
	// patterns included.
	WithCap int `json:"withCap"`
	// WithOwnRestriction counts tenants whose own rule restricts routes; a rule that only lists
	// approved hosts or exceptions restricts none.
	WithOwnRestriction int `json:"withOwnRestriction"`
}

func (a admin) getTenantCounts(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	ctx := r.Context()
	st := a.cp.Store()
	tenants, err := st.ListTenants(ctx)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	count := func(scope string, narrows func(routepolicy.Rule) bool) (int, error) {
		rows, err := st.ListTenantRoutePolicies(ctx, scope)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, row := range rows {
			l, err := layerOf(row)
			if err != nil {
				return 0, err
			}
			if narrows(l.Rule) {
				n++
			}
		}
		return n, nil
	}
	out := tenantCountsWire{Tenants: len(tenants)}
	if out.WithCap, err = count(store.RoutePolicyTenantCap, func(r routepolicy.Rule) bool {
		return r.Restricted() || r.Endpoints != nil
	}); err == nil {
		out.WithOwnRestriction, err = count(store.RoutePolicyTenant, routepolicy.Rule.Restricted)
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// readRule decodes and validates a rule body. above, when set, is the layer the rule may not
// widen (the operator ceiling for a super_admin's write, §8 M1).
func readRule(w http.ResponseWriter, r *http.Request, above *routepolicy.Rule) (routepolicy.Rule, string, bool) {
	var in routepolicy.Rule
	if !decodeBody(w, r, &in) {
		return in, "", false
	}
	if err := in.Validate(); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, codeInvalidRoutePolicy, err.Error()})
		return in, "", false
	}
	in = in.Normalize()
	if above != nil {
		if wider := in.Exceeds(*above); len(wider) > 0 {
			writeAPIErr(w, &APIError{http.StatusBadRequest, codeRoutePolicyExceedsCeiling,
				fmt.Sprintf("the deployment's ceiling forbids routes this rule would allow: %v; a layer below the ceiling can only narrow it", wider)})
			return in, "", false
		}
		if wider := in.EndpointsExceed(*above); len(wider) > 0 {
			writeAPIErr(w, &APIError{http.StatusBadRequest, codeRoutePolicyExceedsCeiling,
				fmt.Sprintf("the deployment's ceiling does not let these endpoint patterns be approved: %v; a layer below the ceiling can only narrow it", wider)})
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

// ifMatch reads the precondition of a PUT: the layer's own revision as the GET answered it
// (policyLayerWire.Revision, 0 for a layer never written), written as an If-Match entity tag,
// quoted or bare. An absent header is no precondition (nil), the unconditional write older
// callers rely on; anything else that is not a non-negative integer is a 400 rather than a
// silent downgrade to unconditional, because a caller that sent a precondition meant it.
func ifMatch(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	vals := r.Header.Values("If-Match")
	if len(vals) == 0 {
		return nil, true
	}
	raw := ""
	if len(vals) == 1 {
		raw = strings.TrimSpace(vals[0])
		if q, ok := strings.CutPrefix(raw, `"`); ok {
			raw, ok = strings.CutSuffix(q, `"`)
			if !ok {
				raw = ""
			}
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || raw != strconv.FormatInt(n, 10) {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request",
			`invalid If-Match: want one revision as the GET answered it, e.g. "12"`})
		return nil, false
	}
	return &n, true
}

// putLayer writes one layer and its audit row (D15: before and after) in one transaction: when
// the record cannot be written, neither is the rule. expect, when set, is the revision the editor
// loaded: the store refuses the write inside its transaction when the row has moved, so two saves
// based on one revision cannot both land.
func (a admin) putLayer(w http.ResponseWriter, r *http.Request, ident store.Identity, scope string, t store.Tenant, rule string, expect *int64) {
	target := t.Slug
	if scope == store.RoutePolicyDeployment {
		target = "deployment"
	}
	cur, after, err := a.cp.Store().PutRoutePolicyIf(r.Context(), scope, t.ID, rule, ident.ID, expect,
		func(before, after store.RoutePolicyRow) store.AuditLog {
			return store.AuditLog{ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
				Action: "route_policy." + scope + ".put", Target: target,
				Detail:     fmt.Sprintf("before=%s after=%s revision=%d", before.Rule, after.Rule, after.Revision),
				HTTPStatus: http.StatusOK, At: store.NowTS()}
		})
	if errors.Is(err, store.ErrRoutePolicyStale) {
		writeAPIErr(w, &APIError{http.StatusConflict, codeRoutePolicyStale,
			fmt.Sprintf("the rule is at revision %d now, not the revision %d it was loaded at; nothing was saved", cur.Revision, *expect)})
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	l, err := layerOf(after)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a admin) putDeployment(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	expect, ok := ifMatch(w, r)
	if !ok {
		return
	}
	c := currentCeiling()
	if _, rule, ok := readRule(w, r, &c.rule); ok {
		a.putLayer(w, r, ident, store.RoutePolicyDeployment, store.Tenant{}, rule, expect)
	}
}

// putCap is the super_admin's cap on one tenant; a tenant_admin can neither write nor clear it.
func (a admin) putCap(w http.ResponseWriter, r *http.Request) {
	ident, ok := a.cp.SuperAdminFor(w, r)
	if !ok {
		return
	}
	t, found, err := a.cp.Store().GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	expect, ok := ifMatch(w, r)
	if !ok {
		return
	}
	c := currentCeiling()
	if _, rule, ok := readRule(w, r, &c.rule); ok {
		a.putLayer(w, r, ident, store.RoutePolicyTenantCap, t, rule, expect)
	}
}

// putTenant is the tenant_admin's own rule. It may name exceptions the layers above do not have:
// those reopen nothing (D1d, D7a), so they are stored as written rather than refused. Its
// endpoints are the hosts it approves as self-hosted (ADR 0138 §2): exact hosts only, each one
// the layers above let through now, and never a vendor's. A layer above that narrows later leaves
// the host stored but no longer approved.
func (a admin) putTenant(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	expect, ok := ifMatch(w, r)
	if !ok {
		return
	}
	in, rule, ok := readRule(w, r, nil)
	if !ok {
		return
	}
	if in.Endpoints != nil {
		if err := in.ExactEndpoints(); err != nil {
			writeAPIErr(w, &APIError{http.StatusBadRequest, codeInvalidRoutePolicy, err.Error()})
			return
		}
		v, err := TenantPolicy(r.Context(), a.cp.Store(), t.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, h := range *in.Endpoints {
			if by := v.layers.EndpointExcludedBy(h); len(by) > 0 {
				writeAPIErr(w, &APIError{http.StatusBadRequest, codeRoutePolicyEndpointNotApprovable,
					fmt.Sprintf("%s cannot be approved as a self-hosted endpoint: excluded by %v", h, by)})
				return
			}
		}
	}
	a.putLayer(w, r, ident, store.RoutePolicyTenant, t, rule, expect)
}

func (a admin) getTenant(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	v, err := TenantPolicy(r.Context(), a.cp.Store(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}
