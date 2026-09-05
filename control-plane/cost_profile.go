// cost_profile.go — does this deployment HAVE an AWS bill, and what of it can be
// attributed to a person (docs/log/67 §67.8, ADR 0048 decision 9).
//
// Same shape as workspace_sizing.go, for the same reason: the Console must stop
// describing every deployment as if it were the AWS one. A docker or native deployment
// has no invoice at all, and a cost screen there would be worse than missing — it would
// be a screen full of zeros that looks like a bug, or worse, like "you cost nothing".
// Showing a figure that means nothing here is close to lying (ADR 0045 decision 21).
package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// runtime.CostProfile is declared alongside the adapters in internal/runtime/profiles.go:
// the CostProfile() methods hang off the factory types, and Go only allows that in their
// own package.

// costProfiler is the optional RuntimeFactory capability, like sizingProfiler.
type costProfiler interface {
	CostProfile() runtime.CostProfile
}

// cloudCostProfile reports the deployment's profile. An adapter that does not declare
// one has no AWS bill — that is the safe default, because the failure mode of guessing
// "available" is showing somebody an empty cost page and letting them conclude the
// deployment is free.
func (m *manager) cloudCostProfile() runtime.CostProfile {
	if f, ok := m.rtFactory.(costProfiler); ok {
		return f.CostProfile()
	}
	return runtime.CostProfile{Runtime: "local"}
}

// costProfileHandler (GET /api/cost/profile) — any signed-in identity may read it. It
// says nothing about money, only what KIND of deployment this is, and the Console needs
// it before it can decide whether to draw the tab at all.
func (a adminAPI) costProfileHandler(w http.ResponseWriter, _ *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.mgr.cloudCostProfile())
}
