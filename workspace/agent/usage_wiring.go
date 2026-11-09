package main

// usage_wiring.go — wires `internal/usagesvc`'s outward dependencies (usagesvc → main) in one
// place.
//
// Never give the wiring defaults: `usagesvc.Configure` panics on anything left unwired.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagesvc"
)

func init() {
	usagesvc.Configure(usageDeps())
	agy.LearnedWindows = func(now time.Time) []agy.LearnedWindow {
		var out []agy.LearnedWindow
		for _, l := range routehealth.Default.LearnedWindows(session.KindAgy, now) {
			out = append(out, agy.LearnedWindow{Model: l.Model, Until: l.Until})
		}
		return out
	}
}

func usageDeps() usagesvc.Deps {
	return usagesvc.Deps{
		ModelsDevPath: modelsDevPath,
	}
}
