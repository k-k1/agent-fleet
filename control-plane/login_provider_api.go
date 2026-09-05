package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The deployment's own login providers, read-only (docs/log/61 §61.11.8).
//
// tenant.allowed_providers is a free-text CSV of provider ids, and saving one the
// deployment does not have is refused with 400 unknown_provider (tenants.go). The
// ids live in env (AF_OIDC_PROVIDERS plus Google's historical names), so before
// this endpoint the only way to learn what may be written there was to read the
// deployment's environment — which the person editing the rule usually cannot.
//
// Nothing here is a credential. The response carries the id, the button labels
// and (for a super_admin) the issuer; client_id and client_secret are deliberately
// absent — an admin API that "just shows the config" is how secrets end up in a
// screenshot. The issuer is the one config value the reader actually needs: it says
// WHICH Entra (or Okta, or Keycloak) the id stands for, which is the same question
// the sign-in method register answers for tenant-defined rows.
//
// Tenant-defined providers ("t:<slug>:<name>", docs/log/61 §61.11) are NOT listed.
// They come and go at runtime, they belong to their tenant, and the generic list
// of them is a directory of the group's subsidiaries (decision 32-4). A tenant's own
// rows are already visible on that tenant's sign-in method panel.
type loginProviderAPI struct {
	memberAuth
	// provs is the startup-fixed set, captured at route registration. config is
	// copied by value into handlers, so this is a snapshot by construction —
	// which is correct here: env-defined providers cannot change without a
	// restart (the runtime half is tenantIdPRegistry, and it is not in this list).
	provs []auth.LoginProvider
}

func newLoginProviderAPI(m *manager, provs []auth.LoginProvider) loginProviderAPI {
	return loginProviderAPI{memberAuth{m}, provs}
}

// providerIssuer is implemented by the built-in provider types so the admin list
// can name the identity source without widening the loginProvider interface —
// which every test fake would then have to grow a method for.
type providerIssuer interface{ IssuerURL() string }

// list (GET /api/admin/providers) — readable by a super_admin or by ANY tenant's
// administrator (anyTenantAdminFor); EDITING the rule this list feeds stays
// super_admin-only (decision 19 is unchanged).
//
// It was super_admin-only until P7 (docs/log/61 §61.17.9 ①), which made the deployment's
// methods the DEFAULT TENANT's methods: every tenant's sign-in method panel now lists
// them, so its administrator has to be able to read them. The ids and button labels
// were never secret — they are on the unauthenticated /login, and
// GET /api/me/login-methods hands the same ids to ordinary members.
//
// The ISSUER is the exception, and it is why this is not simply a gate change.
// "https://login.microsoftonline.com/<tenant GUID>/v2.0" or "https://acme.okta.com"
// names the operator's own directory and does NOT appear on /login. A tenant
// administrator needs to know THAT a method exists, not which directory backs it —
// so the column is omitted unless the caller is a super_admin. Omitted, not blanked:
// a "" issuer would render as an empty cell and read like a misconfiguration.
func (a loginProviderAPI) list(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	withIssuer := ident.Role == "super_admin"
	out := make([]map[string]any, 0, len(a.provs))
	for _, p := range a.provs {
		row := map[string]any{
			"id": p.ID(),
			// Both languages, resolved the same way the login buttons resolve
			// them (AF_OIDC_<ID>_LABEL_JA/EN, else a default built from the id).
			// The Console picks by its own locale — it must not have to guess a
			// label from the id, which is exactly what this endpoint exists to
			// stop.
			"label_ja": p.Label("ja"),
			"label_en": p.Label("en"),
		}
		if iss, ok := p.(providerIssuer); ok && withIssuer {
			row["issuer"] = iss.IssuerURL()
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}
