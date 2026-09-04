// tenant_network.go — the tenant's own source-network restriction: read it, and write
// it without letting the person writing it lock their tenant out (docs/log/66, ADR 0047).
//
// It is a separate endpoint from setTenantLogin, which is super_admin-only because its
// three fields reach OUTSIDE the tenant (an auto-join domain widens the deployment's
// entry gate; allowed_providers decides which IdP is trusted to say who someone is).
// This one reaches nothing outside the tenant, so by the same line it belongs to the
// tenant_admin (ADR 0043 decision 24/25 → ADR 0047 decision 6).
package tenantsrv

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TenantNetwork (GET /api/admin/tenants/{slug}/network) returns the stored rule plus
// what the CP can see about the CALLER. The two are inseparable in practice: a list of
// CIDRs means nothing to an administrator who cannot check it against the address this
// deployment actually attributes to them.
func (a Admin) TenantNetwork(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	cidrs, err := a.cp.Store().GetTenantAllowedCIDRs(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	info := a.cp.ClientIPFrom(r.Context())
	hops := a.cp.TrustedProxyHops()
	out := tenantNetworkWire{
		Tenant:       t.Slug,
		AllowedCIDRs: cidrs,
		ProxyHops:    hops,
		// your_ip is what a rule would be matched against, NOT what the browser thinks
		// its address is. Showing anything else would be a comfortable lie.
		YourIP: "",
		// editable is false when this deployment cannot name its callers. The screen
		// then explains it instead of offering a control that would either do nothing
		// or lock everybody out; the save endpoint refuses for the same reason, so a
		// stale page cannot get past it.
		Editable: info.OK && !(hops <= 0 && info.Forwarded),
		Reason:   proxyMisconfigReason(hops, info),
	}
	if info.OK {
		out.YourIP = info.IP.String()
	}
	writeJSON(w, http.StatusOK, out)
}

// proxyMisconfigReason names the two ways the CP cannot be trusted to identify a
// caller, and "" when it can.
//
//   - proxy_not_configured: a forwarding header arrived but the deployment says there
//     is no proxy, so the CP is attributing every request to the load balancer's own
//     address. This is the dangerous one: an administrator who allowlists the
//     address shown to them would be allowing the load balancer — that is, everybody —
//     while believing they had restricted the tenant.
//   - client_ip_unknown: the deployment declares N proxies and the chain is shorter
//     than that, so there is nothing to read at position N.
//
// hops is AF_TRUSTED_PROXY_HOPS, passed in rather than read from a package global:
// the value lives in the CP (clientip.go), which is where the tests set it.
func proxyMisconfigReason(hops int, info ClientIP) string {
	switch {
	case hops <= 0 && info.Forwarded:
		return "proxy_not_configured"
	case !info.OK:
		return "client_ip_unknown"
	}
	return ""
}

// SetTenantNetwork (PUT /api/admin/tenants/{slug}/network) stores the rule.
//
// Three refusals, in this order, and each of them is a lockout the product would
// otherwise have created (ADR 0047 decision 4):
//
//  1. the deployment cannot name callers → refuse ANY non-empty rule
//  2. an entry is not an address or a prefix → refuse (a typo is silent otherwise)
//  3. the editor's own address is not covered → refuse, and say the address
//
// Clearing the rule (empty list) is always allowed, from anywhere. That is the way
// back for a tenant_admin who is still inside the allowed network and wants out of the
// restriction, and it must not itself be gated on the checks above.
func (a Admin) SetTenantNetwork(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		AllowedCIDRs string `json:"allowed_cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	prefixes, normalized, aerr := a.cp.ParseCIDRList(body.AllowedCIDRs)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if len(prefixes) > 0 {
		info := a.cp.ClientIPFrom(r.Context())
		switch proxyMisconfigReason(a.cp.TrustedProxyHops(), info) {
		case "proxy_not_configured":
			writeAPIErr(w, &APIError{http.StatusBadRequest, "proxy_not_configured",
				"a proxy sits in front of the control plane but AF_TRUSTED_PROXY_HOPS is not set, " +
					"so every request looks like it comes from that proxy; ask the operator to set it before restricting networks"})
			return
		case "client_ip_unknown":
			writeAPIErr(w, &APIError{http.StatusBadRequest, "client_ip_unknown",
				"the control plane cannot determine the source address of this request, so a network rule could not be enforced"})
			return
		}
		if !a.cp.IPInAny(info.IP, prefixes) {
			writeAPIErr(w, &APIError{http.StatusBadRequest, "would_lock_out",
				"your own address (" + info.IP.String() + ") is not in this list; add it before saving, " +
					"otherwise this tenant becomes unreachable for you"})
			return
		}
	}
	stored := strings.Join(normalized, ",")
	if err := a.cp.Store().SetTenantAllowedCIDRs(r.Context(), t.ID, stored); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// This rule IS consulted on every request through the cache; a stale copy would
	// keep letting the old networks in (and, worse, keep locking the new ones out).
	a.cp.InvalidateTenantLogin()
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.network_rules", Target: t.Slug,
		Detail: "allowed_cidrs=" + stored, At: store.NowTS(),
	})
	// The normalized text goes back so the screen shows what was actually stored:
	// 192.0.2.7/24 is saved as 192.0.2.0/24, and an editor who is not told would keep
	// believing the rule says something it does not.
	writeJSON(w, http.StatusOK, tenantNetworkSavedWire{Tenant: t.Slug, AllowedCIDRs: stored})
}

// tenantNetworkWire is the GET /api/admin/tenants/{slug}/network response, read by the
// Console's `NetworkView` (console/src/features/settings/tenant/tenantNetwork.tsx).
//
// was: map[string]any{"tenant":…, "allowed_cidrs":…, "proxy_hops":…, "your_ip":"",
// "editable":…, "reason":…}, with your_ip overwritten when info.OK.
//
// your_ip is always seeded with "" and only then overwritten, so all six keys are
// unconditional and none of them takes omitempty; a scan that reads the assignment
// inside the if marks it conditional, which overstates it. reason may legitimately
// be "". tenant echoes the slug the Console already holds as a prop — `NetworkView`
// does not read it, so typing it changes neither the wire nor the screen.
type tenantNetworkWire struct {
	Tenant       string `json:"tenant"`
	AllowedCIDRs string `json:"allowed_cidrs"`
	ProxyHops    int    `json:"proxy_hops"`
	YourIP       string `json:"your_ip"`
	Editable     bool   `json:"editable"`
	Reason       string `json:"reason"`
}

// tenantNetworkSavedWire is the PUT response: the normalized text as stored
// (192.0.2.7/24 → 192.0.2.0/24). Its key set differs from the GET, hence a separate type.
//
// was: map[string]any{"tenant":…, "allowed_cidrs":…}
type tenantNetworkSavedWire struct {
	Tenant       string `json:"tenant"`
	AllowedCIDRs string `json:"allowed_cidrs"`
}
