package main

// docs/56 P0: read-only view of one working copy's project-scope MCP servers
// (internal/mcpproj). This is the "management axis" (docs/57 §0) — deliberately
// separate from internal/mcpreg's automatic user/global materialize, which never
// runs against a repo directory and is never called from here.

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpproj"
)

// handleRepoMCP serves GET /repos/{name}/mcp — the docs/56 §10 snapshot endpoint.
// It accepts either VCS (git or svn, like the other read-only SCM-adjacent
// endpoints) since mcpproj's job is only to read files, not to run VCS-specific
// commands beyond tracked/ignored detection (which itself degrades to "uncertain"
// off git — docs/56 §7.2).
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
