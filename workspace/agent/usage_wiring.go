package main

// usage_wiring.go — wires `internal/usagesvc`'s outward dependencies (usagesvc → main) in one
// place.
//
// Never give the wiring defaults: `usagesvc.Configure` panics on anything left unwired.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/usagesvc"

func init() { usagesvc.Configure(usageDeps()) }

func usageDeps() usagesvc.Deps {
	return usagesvc.Deps{
		ModelsDevPath: modelsDevPath,
	}
}
