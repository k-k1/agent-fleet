package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/climanifest"
)

// Every CLI in the manifest has a toolSpecs row whose Pin key is the versions.json key the
// Dockerfile writes for it, which is also the manifest name (mcp_grafana is the key of the
// row named mcp-grafana). Adding a CLI to the manifest without its row here fails.
func TestCLIManifestToolSpecs(t *testing.T) {
	byPin := map[string]toolSpec{}
	for _, s := range toolSpecs {
		byPin[s.Pin] = s
	}
	for _, e := range climanifest.Entries() {
		if _, ok := byPin[e.Name]; !ok {
			t.Errorf("%s: no toolSpecs row with Pin %q (the versions.json key)", e.Name, e.Name)
		}
	}
}
