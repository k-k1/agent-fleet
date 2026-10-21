package main

import (
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// mcpConnectedAPI exposes display-only connection metadata and owner-scoped revocation.
type mcpConnectedAPI struct{ memberAuth }

// Only display fields cross the API boundary; store grants also carry code hashes.
type mcpConnectedGrant struct {
	ID           string `json:"id"`
	ClientName   string `json:"client_name"`
	RedirectHost string `json:"redirect_host"`
	Tenant       string `json:"tenant"`
	Scope        string `json:"scope"`
	CreatedAt    int64  `json:"created_at"`
	LastUsedAt   int64  `json:"last_used_at"`
}

type mcpConnectedList struct {
	Enabled bool                `json:"enabled"`
	Grants  []mcpConnectedGrant `json:"grants"`
}

func (a mcpConnectedAPI) list(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	noStore(w)
	out := mcpConnectedList{Enabled: a.mgr.mcpOAuthServed.Load(), Grants: []mcpConnectedGrant{}}
	if !out.Enabled {
		writeJSON(w, http.StatusOK, out)
		return
	}
	grants, err := a.mgr.store.ListMCPOAuthGrantsByIdentity(r.Context(), ident.ID, time.Now().Unix())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, g := range grants {
		tenant, err := a.mgr.store.GetTenant(r.Context(), g.TenantID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		out.Grants = append(out.Grants, mcpConnectedGrant{g.ID, g.ClientName, g.RedirectHost, tenant.Slug, g.Scope, g.CreatedAt, g.LastUsedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a mcpConnectedAPI) revoke(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	noStore(w)
	id := r.PathValue("id")
	revoked, err := a.mgr.store.RevokeMCPOAuthGrantOf(r.Context(), id, ident.ID, "revoked by member", time.Now().Unix())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !revoked {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "unknown connection"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
