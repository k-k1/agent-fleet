package main

// node_latest_http.go — the Node.js twin of jdk-latest: which patch Install would fetch
// for a major, and whether it is newer than the one sessions get now. Kept out of the
// toolchains GET for the same reason: it needs nodejs.org, and an offline workspace
// must still open the tab instantly.

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

var nodeLatestLookup = newVersionLookup(latestNodePatch)

// handleNodeLatest is GET /env/node-latest?major=22. Versions carry nvm's "v" prefix
// ("v22.23.4"), as the directory names and latestNodePatch do.
func handleNodeLatest(w http.ResponseWriter, r *http.Request) {
	major := r.URL.Query().Get("major")
	// The major reaches a URL and a cache key, so only the numeric majors the picker
	// offers ("system" has nothing to fetch).
	if !majorOnlyRe.MatchString(major) || !isOffered(major, nodeOptionsFor(readToolchains().Node)) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid node major version")
		return
	}
	current := ""
	if bin := nodeBinFor(major); bin != "" {
		current = filepath.Base(filepath.Dir(bin))
	}
	latest, err := nodeLatestLookup.latest(major)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "node_latest_unavailable", err.Error())
		return
	}
	update := false
	if current != "" {
		cur, lat := parseDotted(strings.TrimPrefix(current, "v")), parseDotted(strings.TrimPrefix(latest, "v"))
		update = cur != nil && lat != nil && compareDotted(lat, cur) > 0
	}
	httpx.WriteJSON(w, http.StatusOK, toolchainLatestReply{
		Major:           major,
		Current:         current,
		Latest:          latest,
		UpdateAvailable: update,
	})
}
