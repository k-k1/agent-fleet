// cost_profile.go — does this deployment HAVE an AWS bill, and what of it can be
// attributed to a person (docs/67 §67.8, ADR 0048 決定 9).
//
// Same shape as workspace_sizing.go, for the same reason: the Console must stop
// describing every deployment as if it were the AWS one. A docker or native deployment
// has no invoice at all, and a cost screen there would be worse than missing — it would
// be a screen full of zeros that looks like a bug, or worse, like "you cost nothing".
// "効かない項目を画面に出すのは嘘に近い" (ADR 0045 決定 21).
package main

import "net/http"

// costProfile is the runtime's answer to "is there money to show here, and what does it
// cover". Everything in it is a claim the Console is allowed to print.
type costProfile struct {
	Runtime string `json:"runtime"`
	// Available is the whole gate: false = no cost surface anywhere in the UI.
	Available bool `json:"available"`
	// Attributable lists what actually carries `af-membership`, so the Console can name
	// what a member's number covers instead of implying it covers everything. Measured
	// on the reference deployment: what CAN be attributed is about a fifth of the bill
	// (docs/67 §67.3), and the rest is shared — never divided (ADR 0048 決定 4).
	Attributable []string `json:"attributable,omitempty"`
	// Shared lists the big cost centres that belong to nobody. Shown only to a
	// super_admin, but declared here so the member-facing hint can say what is EXCLUDED
	// without the Console hard-coding a list of AWS service names.
	Shared []string `json:"shared,omitempty"`
	// Verified is false where the tagging exists in code but has never been observed on
	// a real deployment (Fargate). A number nobody has ever seen arrive should not be
	// presented with the same confidence as one that has.
	Verified bool `json:"verified"`
}

// cost centre labels. Kept as stable identifiers rather than prose so the Console can
// translate them; the Console owns the wording in both languages.
const (
	costCentreSlotHours   = "slot_hours"   // EC2 instance-hours while a home is attached
	costCentreHomeVolume  = "home_volume"  // the member's persistent EBS home
	costCentreSnapshots   = "snapshots"    // hibernation + backup snapshots of that home
	costCentreTaskCompute = "task_compute" // Fargate task vCPU/GB-hours
	costCentreScratch     = "scratch"      // ECS-managed EBS working disk

	costCentreNAT      = "nat"       // the single biggest shared line, and untaggable
	costCentreDNS      = "dns"       // Route53 hosted zone + queries
	costCentreLB       = "lb"        // ALB
	costCentreDB       = "db"        // RDS
	costCentreEFS      = "efs"       // billed per filesystem, so it cannot be split
	costCentreIdlePool = "idle_pool" // warm slots nobody is holding
	costCentreCP       = "cp"        // the control plane's own task
	costCentreTax      = "tax"
)

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

// CostProfile — docker: the operator's own hardware. There is no invoice to read.
func (f *dockerFactory) CostProfile() costProfile { return costProfile{Runtime: "local"} }

// CostProfile — native: same, containerless.
func (f *nativeFactory) CostProfile() costProfile { return costProfile{Runtime: "native"} }

// CostProfile — Fargate. The task is the billed unit and it now inherits af-membership
// from its service, but ⚠️ this has never run against real Fargate: the deployment this
// was developed on is ecs-ec2 (ADR 0048 決定 9). Reported as unverified rather than
// quietly claimed.
func (f *ecsFactory) CostProfile() costProfile {
	return costProfile{
		Runtime: "ecs", Available: true, Verified: false,
		Attributable: []string{costCentreTaskCompute, costCentreScratch},
		Shared: []string{costCentreNAT, costCentreDNS, costCentreLB, costCentreDB,
			costCentreEFS, costCentreCP, costCentreTax},
	}
}

// CostProfile — the EC2 slot pool, the one that has been measured end to end. A slot is
// used by exactly one person while their home is attached (ADR 0045 決定 8), which is
// what makes instance-hours attributable at all; an unclaimed warm slot is shared, and
// showing that is the point rather than a caveat (it is the price of the pool size).
func (f *ecsEC2Factory) CostProfile() costProfile {
	return costProfile{
		Runtime: "ecs-ec2", Available: true, Verified: true,
		Attributable: []string{costCentreSlotHours, costCentreHomeVolume, costCentreSnapshots},
		Shared: []string{costCentreNAT, costCentreDNS, costCentreLB, costCentreDB,
			costCentreEFS, costCentreIdlePool, costCentreCP, costCentreTax},
	}
}

// costProfileHandler (GET /api/cost/profile) — any signed-in identity may read it. It
// says nothing about money, only what KIND of deployment this is, and the Console needs
// it before it can decide whether to draw the tab at all.
func (a adminAPI) costProfileHandler(w http.ResponseWriter, _ *http.Request, _ Identity) {
	writeJSON(w, http.StatusOK, a.mgr.cloudCostProfile())
}
