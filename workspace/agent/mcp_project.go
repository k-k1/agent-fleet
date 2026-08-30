package main

// docs/log/56 P0: read-only view of one working copy's project-scope MCP servers
// (internal/mcpproj). This is the "management axis" (docs/log/57 §0) — deliberately
// separate from internal/mcpreg's automatic user/global materialize, which never
// runs against a repo directory and is never called from here.

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpproj"
)

// handleRepoMCP serves GET /repos/{name}/mcp — the docs/log/56 §10 snapshot endpoint.
// It accepts either VCS (git or svn, like the other read-only SCM-adjacent
// endpoints) since mcpproj's job is only to read files, not to run VCS-specific
// commands beyond tracked/ignored detection (which itself degrades to "uncertain"
// off git — docs/log/56 §7.2).
func handleRepoMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dir, ok := repoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	snap, err := mcpproj.Inspect(dir, name)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mcp_project_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snap)
}

type mcpPlanRequest struct {
	Ops []mcpproj.Op `json:"ops"`
}

type mcpApplyRequest struct {
	Ops      []mcpproj.Op `json:"ops"`
	PlanHash string       `json:"planHash"`
}

// handleRepoMCPPlan serves POST /repos/{name}/mcp/plan (docs/log/56 §5/§10): computes
// what the given ops would do WITHOUT writing anything, returning a masked
// preview, warnings, and a planHash apply must echo back.
func handleRepoMCPPlan(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	var req mcpPlanRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := mcpproj.Plan(dir, req.Ops)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mcp_project_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleRepoMCPApply serves POST /repos/{name}/mcp/apply: the same ops plus the
// planHash from a prior plan call. A 409 means a file the ops would write has
// changed since that plan was computed (docs/log/56 §5's optimistic lock) — the
// caller should plan again and let the user re-confirm, never silently retry.
func handleRepoMCPApply(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	var req mcpApplyRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := mcpproj.Apply(dir, req.Ops, req.PlanHash)
	if err != nil {
		if err == mcpproj.ErrPlanStale {
			httpx.WriteErr(w, http.StatusConflict, "mcp_project_plan_stale", err.Error())
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "mcp_project_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
