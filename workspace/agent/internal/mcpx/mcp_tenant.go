package mcpx

// When the tenant-distributed MCP set is fetched (docs/log/48 §6 / P4). Fetching the
// definitions themselves lives in internal/mcpreg/tenant.go; this file holds only "when to
// pull" - the same split as mcp_materialize.go holding only the triggers for materialize.
//
// Three triggers:
//   - agent boot (start from whatever the tenant distributes at that moment)
//   - polling every 5 minutes (the only route by which an admin's registration arrives
//     without anyone doing anything)
//   - an explicit refresh from the Console (the shortest path to "apply it now")
//
// materialize runs only when the fetched content actually changed. Writing on every pull
// would stamp on .claude.json - which claude itself rewrites constantly - every 5 minutes
// (§8.2).
//
// An unreachable CP leaves things as they are rather than being an error (fail-open, §6).
// Fail-closed here would make MCP vanish from every member's session on a CP blip.

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// StartTenantSync fetches the distributed set once, then keeps polling. The first
// fetch is in the goroutine too: boot must not block on the CP being reachable.
func StartTenantSync() {
	go func() {
		syncMCPTenant("agent boot")
		for range time.Tick(mcpreg.TenantPollInterval) {
			syncMCPTenant("poll")
		}
	}()
}

// syncMCPTenant pulls once and materializes when the set changed.
func syncMCPTenant(why string) mcpreg.TenantFetchResult {
	res, err := mcpreg.FetchTenant()
	switch {
	case errors.Is(err, mcpreg.ErrTenantBridgeOff):
		// Not configured in this deployment (no PUBLIC_BASE_URL). Normal, not a fault.
		return res
	case err != nil:
		// Keep the previous cache and say so once per attempt — a member whose tenant
		// servers went missing needs this line to exist somewhere.
		log.Printf("mcp tenant sync (%s): %v (keeping cached set)", why, err)
		return res
	}
	if res.Dropped > 0 {
		log.Printf("mcp tenant sync (%s): dropped %d definition(s) refused by local validation", why, res.Dropped)
	}
	if res.Changed {
		log.Printf("mcp tenant sync (%s): %d server(s), materializing", why, res.Servers)
		MaterializeAll()
	}
	return res
}

// HandleTenantRefresh (POST /mcp-servers/tenant-refresh) pulls the tenant set now and
// returns the refreshed registry, so the Console's refresh button shows the outcome in
// one round trip. A fetch failure is reported as a 502 with the CP's own message: the
// member can act on "the CP said 401" but not on a silently stale list.
func HandleTenantRefresh(w http.ResponseWriter, r *http.Request) {
	res, err := mcpreg.FetchTenant()
	if errors.Is(err, mcpreg.ErrTenantBridgeOff) {
		httpx.WriteErr(w, http.StatusNotImplemented, "mcp_tenant_bridge_off", err.Error())
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "mcp_tenant_fetch_failed", err.Error())
		return
	}
	if res.Changed {
		MaterializeAll()
	}
	reg, lerr := mcpreg.Load()
	if lerr != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", lerr.Error())
		return
	}
	out := make([]mcpServerWire, 0, len(reg.Servers))
	for _, d := range reg.Servers {
		out = append(out, wireMCPServer(d))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"servers":         out,
		"tenantFetchedAt": reg.TenantFetchedAt,
		"shadowed":        reg.Shadowed,
		"fetch":           res,
	})
}

// HandleServerSecrets (PUT /mcp-servers/{id}/secrets) stores the member's own header
// values for a tenant-distributed user_secret definition (docs/log/48 §5.2). This is the only
// write a member has into a tenant row's content, and it lands in the member's own
// encrypted store — the distributed definition is never modified.
func HandleServerSecrets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Headers map[string]string `json:"headers"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := mcpreg.SetTenantSecrets(r.PathValue("id"), req.Headers); err != nil {
		writeMCPErr(w, err)
		return
	}
	// Filling in a value can flip a server from "held back" to usable, so write the CLI
	// configs before answering — otherwise the member has to launch two sessions.
	MaterializeAll()
	HandleServersGet(w, r)
}
