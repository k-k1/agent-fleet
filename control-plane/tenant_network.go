// tenant_network.go — the tenant's own source-network restriction: read it, and write
// it without letting the person writing it lock their tenant out (docs/log/66, ADR 0047).
//
// It is a separate endpoint from setTenantLogin, which is super_admin-only because its
// three fields reach OUTSIDE the tenant (an auto-join domain widens the deployment's
// entry gate; allowed_providers decides which IdP is trusted to say who someone is).
// This one reaches nothing outside the tenant, so by the same line it belongs to the
// tenant_admin (ADR 0043 決定 24/25 → ADR 0047 決定 6).
package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// tenantNetwork (GET /api/admin/tenants/{slug}/network) returns the stored rule plus
// what the CP can see about the CALLER. The two are inseparable in practice: a list of
// CIDRs means nothing to an administrator who cannot check it against the address this
// deployment actually attributes to them.
func (a adminAPI) tenantNetwork(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	cidrs, err := a.mgr.store.GetTenantAllowedCIDRs(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	info := clientIPFrom(r.Context())
	out := map[string]any{
		"tenant":        t.Slug,
		"allowed_cidrs": cidrs,
		"proxy_hops":    trustedProxyHops,
		// your_ip is what a rule would be matched against, NOT what the browser thinks
		// its address is. Showing anything else would be a comfortable lie.
		"your_ip": "",
		// editable is false when this deployment cannot name its callers. The screen
		// then explains it instead of offering a control that would either do nothing
		// or lock everybody out; the save endpoint refuses for the same reason, so a
		// stale page cannot get past it.
		"editable": info.OK && !(trustedProxyHops <= 0 && info.Forwarded),
		"reason":   proxyMisconfigReason(info),
	}
	if info.OK {
		out["your_ip"] = info.IP.String()
	}
	writeJSON(w, http.StatusOK, out)
}

// proxyMisconfigReason names the two ways the CP cannot be trusted to identify a
// caller, and "" when it can.
//
//   - proxy_not_configured: a forwarding header arrived but the deployment says there
//     is no proxy, so the CP is attributing every request to the load balancer's own
//     address. ⚠️ This is the dangerous one: an administrator who allowlists the
//     address shown to them would be allowing the load balancer — that is, everybody —
//     while believing they had restricted the tenant.
//   - client_ip_unknown: the deployment declares N proxies and the chain is shorter
//     than that, so there is nothing to read at position N.
func proxyMisconfigReason(info clientIPInfo) string {
	switch {
	case trustedProxyHops <= 0 && info.Forwarded:
		return "proxy_not_configured"
	case !info.OK:
		return "client_ip_unknown"
	}
	return ""
}

// setTenantNetwork (PUT /api/admin/tenants/{slug}/network) stores the rule.
//
// Three refusals, in this order, and each of them is a lockout the product would
// otherwise have created (ADR 0047 決定 4):
//
//  1. the deployment cannot name callers → refuse ANY non-empty rule
//  2. an entry is not an address or a prefix → refuse (a typo is silent otherwise)
//  3. the editor's own address is not covered → refuse, and say the address
//
// Clearing the rule (empty list) is always allowed, from anywhere. That is the way
// back for a tenant_admin who is still inside the allowed network and wants out of the
// restriction, and it must not itself be gated on the checks above.
func (a adminAPI) setTenantNetwork(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		AllowedCIDRs string `json:"allowed_cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	prefixes, normalized, aerr := parseCIDRList(body.AllowedCIDRs)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if len(prefixes) > 0 {
		info := clientIPFrom(r.Context())
		switch proxyMisconfigReason(info) {
		case "proxy_not_configured":
			writeAPIErr(w, &apiError{http.StatusBadRequest, "proxy_not_configured",
				"a proxy sits in front of the control plane but AF_TRUSTED_PROXY_HOPS is not set, " +
					"so every request looks like it comes from that proxy; ask the operator to set it before restricting networks"})
			return
		case "client_ip_unknown":
			writeAPIErr(w, &apiError{http.StatusBadRequest, "client_ip_unknown",
				"the control plane cannot determine the source address of this request, so a network rule could not be enforced"})
			return
		}
		if !ipInAny(info.IP, prefixes) {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "would_lock_out",
				"your own address (" + info.IP.String() + ") is not in this list; add it before saving, " +
					"otherwise this tenant becomes unreachable for you"})
			return
		}
	}
	stored := strings.Join(normalized, ",")
	if err := a.mgr.store.SetTenantAllowedCIDRs(r.Context(), t.ID, stored); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// This rule IS consulted on every request through the cache; a stale copy would
	// keep letting the old networks in (and, worse, keep locking the new ones out).
	a.mgr.tenantLogin.invalidate()
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.network_rules", Target: t.Slug,
		Detail: "allowed_cidrs=" + stored, At: nowTS(),
	})
	// The normalized text goes back so the screen shows what was actually stored:
	// 192.0.2.7/24 is saved as 192.0.2.0/24, and an editor who is not told would keep
	// believing the rule says something it does not.
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "allowed_cidrs": stored})
}
