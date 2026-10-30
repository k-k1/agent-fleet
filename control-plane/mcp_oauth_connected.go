package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
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
	// Endpoint is the resource the connection is bound to, by name: "mcp" or "memory" (#2764).
	// The resource URL itself stays off the wire.
	Endpoint   string `json:"endpoint"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
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
		out.Grants = append(out.Grants, mcpConnectedGrant{g.ID, g.ClientName, g.RedirectHost, tenant.Slug, g.Scope,
			grantResourceKind(g.Resource), g.CreatedAt, g.LastUsedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// calls (GET /api/mcp-oauth/grants/{id}/calls) is the recent /mcp requests of one of the caller's
// own connections, newest first. Another member's id answers 404 exactly like an unknown one.
// The ring lives in this replica's memory (mcpsrv/mcp_calls.go), so behind several replicas it
// holds only the requests that replica served.
func (a mcpConnectedAPI) calls(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	noStore(w)
	id := r.PathValue("id")
	grants, err := a.mgr.store.ListMCPOAuthGrantsByIdentity(r.Context(), ident.ID, time.Now().Unix())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, g := range grants {
		if g.ID == id {
			writeJSON(w, http.StatusOK, mcpCallsOut{Calls: a.mgr.mcpCalls.Recent(mcpsrv.CallKey(true, id))})
			return
		}
	}
	writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "unknown connection"})
}

// grantResourceKind names a grant's resource URL the way a PAT's binding is named. Grants made
// before the memory endpoint existed are all bound to /mcp.
func grantResourceKind(resource string) string {
	if strings.HasSuffix(resource, mcpsrv.MemoryPath) {
		return store.PATResourceMemory
	}
	return store.PATResourceMCP
}

// mcpCallsOut is the body of the two "recent calls" endpoints.
type mcpCallsOut struct {
	Calls []mcpsrv.CallRecord `json:"calls"`
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
	a.mgr.mcpCalls.Forget(mcpsrv.CallKey(true, id))
	w.WriteHeader(http.StatusNoContent)
}
