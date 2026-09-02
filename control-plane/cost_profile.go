// cost_profile.go — does this deployment HAVE an AWS bill, and what of it can be
// attributed to a person (docs/log/67 §67.8, ADR 0048 決定 9).
//
// Same shape as workspace_sizing.go, for the same reason: the Console must stop
// describing every deployment as if it were the AWS one. A docker or native deployment
// has no invoice at all, and a cost screen there would be worse than missing — it would
// be a screen full of zeros that looks like a bug, or worse, like "you cost nothing".
// "効かない項目を画面に出すのは嘘に近い" (ADR 0045 決定 21).
package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// costProfile is declared by the adapters' package: cost_profile.go used to hang a
// CostProfile() method on each of the four factory types, and Go only allows that in
// the package that declares them (internal/runtime/profiles.go). The alias keeps the
// CP-side name — cloudcost.go embeds it, and the JSON is unchanged.
type costProfile = runtime.CostProfile

// costProfiler is the optional RuntimeFactory capability, like sizingProfiler.
type costProfiler interface {
	CostProfile() costProfile
}

// cloudCostProfile reports the deployment's profile. An adapter that does not declare
// one has no AWS bill — that is the safe default, because the failure mode of guessing
// "available" is showing somebody an empty cost page and letting them conclude the
// deployment is free.
func (m *manager) cloudCostProfile() costProfile {
	if f, ok := m.rtFactory.(costProfiler); ok {
		return f.CostProfile()
	}
	return costProfile{Runtime: "local"}
}

// costProfileHandler (GET /api/cost/profile) — any signed-in identity may read it. It
// says nothing about money, only what KIND of deployment this is, and the Console needs
// it before it can decide whether to draw the tab at all.
func (a adminAPI) costProfileHandler(w http.ResponseWriter, _ *http.Request, _ Identity) {
	writeJSON(w, http.StatusOK, a.mgr.cloudCostProfile())
}
