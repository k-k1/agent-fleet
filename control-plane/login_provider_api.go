package main

import (
	"net/http"
)

// The deployment's own login providers, read-only (docs/61 §61.11.8).
//
// tenant.allowed_providers is a free-text CSV of provider ids, and saving one the
// deployment does not have is refused with 400 unknown_provider (tenants.go). The
// ids live in env (AF_OIDC_PROVIDERS plus Google's historical names), so before
// this endpoint the only way to learn what may be written there was to read the
// deployment's environment — which the person editing the rule usually cannot.
//
// ★ Nothing here is a credential. The response carries the id, the button labels
// and the issuer; client_id and client_secret are deliberately absent — an admin
// API that "just shows the config" is how secrets end up in a screenshot. The
// issuer is the one config value the reader actually needs: it says WHICH Entra
// (or Okta, or Keycloak) the id stands for, which is the same question the
// sign-in method register answers for tenant-defined rows.
//
// ★ Tenant-defined providers ("t:<slug>:<name>", docs/61 §61.11) are NOT listed.
// They come and go at runtime, they belong to their tenant, and the generic list
// of them is a directory of the group's subsidiaries (決定 32-4). A tenant's own
// rows are already visible on that tenant's sign-in method panel.
type loginProviderAPI struct {
	memberAuth
	// provs is the startup-fixed set, captured at route registration. config is
	// copied by value into handlers, so this is a snapshot by construction —
	// which is correct here: env-defined providers cannot change without a
	// restart (the runtime half is tenantIdPRegistry, and it is not in this list).
	provs []loginProvider
}

func newLoginProviderAPI(m *manager, provs []loginProvider) loginProviderAPI {
	return loginProviderAPI{memberAuth{m}, provs}
}

// providerIssuer is implemented by the built-in provider types so the admin list
// can name the identity source without widening the loginProvider interface —
// which every test fake would then have to grow a method for.
type providerIssuer interface{ issuerURL() string }

// list (GET /api/admin/providers) — super_admin only, like every other
// deployment-wide admin read. Not because the ids are secret (they appear on the
// login page as buttons), but because the caller is asking about the DEPLOYMENT,
// and 決定 19 already puts the rule that consumes this list behind the same gate:
// a tenant_admin who cannot edit allowed_providers has nothing to do with it.
func (a loginProviderAPI) list(w http.ResponseWriter, r *http.Request, _ Identity) {
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
		if iss, ok := p.(providerIssuer); ok {
			row["issuer"] = iss.issuerURL()
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}
