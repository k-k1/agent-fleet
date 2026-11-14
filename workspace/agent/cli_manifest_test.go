package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/climanifest"
)

// Every CLI in the manifest has a toolSpecs row under its own name whose Pin key is the
// versions.json key the Dockerfile writes for it. Adding a CLI to the manifest without its
// row here fails.
func TestCLIManifestToolSpecs(t *testing.T) {
	byName := map[string]toolSpec{}
	for _, s := range toolSpecs {
		byName[s.Name] = s
	}
	for _, e := range climanifest.Entries() {
		s, ok := byName[e.Name]
		if !ok {
			t.Errorf("%s: no toolSpecs row", e.Name)
			continue
		}
		if s.Pin != e.Name {
			t.Errorf("%s: toolSpecs Pin = %q, want %q (the versions.json key)", e.Name, s.Pin, e.Name)
		}
	}
}
