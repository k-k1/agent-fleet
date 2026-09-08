package runtime

import "testing"

// CostRoleGroup is what turns a list of af-role tag values into the answer the shared cost
// card gives (ADR 0048 decision 15). Two things it must not do: fold a value it does not
// recognise into the platform residual (that is where NAT and RDS live, and a new tag value
// quietly joining them is money nobody ever asks about again), and mis-place the engines,
// which are the reason the breakdown exists.
func TestCostRoleGroupPlacesEveryRoleThisProductWrites(t *testing.T) {
	for role, want := range map[string]string{
		// 60-engines.yaml — the capacity providers and services, plus the bucket and log
		// group. The prefix rule means a third engine role needs no code change here.
		"engine-llm":    CostRoleGroupEngine,
		"engine-image":  CostRoleGroupEngine,
		"engine-models": CostRoleGroupEngine,
		"engine-logs":   CostRoleGroupEngine,
		"engine-comfy":  CostRoleGroupEngine, // ADR 0071 P2, not shipped yet
		// 50-tts.yaml. Deliberately NOT engine-tts: renaming the value now would split
		// its history in the bill, where the old one keeps the spend it already has.
		"tts-engine": CostRoleGroupTTS,
		// Workspace resources appearing in the SHARED bucket means nobody is holding
		// them — an unclaimed warm slot, the golden snapshot, an orphaned home.
		ec2RoleHome:            CostRoleGroupPool,
		ec2RoleSlot:            CostRoleGroupPool,
		ec2RoleBackup:          CostRoleGroupPool,
		ec2RoleQuarantined:     CostRoleGroupPool,
		EC2RoleGolden:          CostRoleGroupPool,
		EC2RoleGoldenCandidate: CostRoleGroupPool,
		EC2RoleGoldenRejected:  CostRoleGroupPool,
		// No af-role at all: NAT, ALB, RDS, Route53, tax. Not a gap — these cannot carry
		// one, and naming that is the point of the row.
		"": CostRoleGroupPlatform,
	} {
		if got := CostRoleGroup(role); got != want {
			t.Errorf("CostRoleGroup(%q) = %q, want %q", role, got, want)
		}
	}
	// An unknown value becomes a question, not part of the residual.
	if got := CostRoleGroup("something-new"); got != CostRoleGroupOther {
		t.Errorf("an unrecognised role landed in %q rather than %q", got, CostRoleGroupOther)
	}
	// "engine" without the hyphen is not an engine role: matching it would swallow any
	// future tag value that merely starts with the word.
	if got := CostRoleGroup("engineering"); got != CostRoleGroupOther {
		t.Errorf("CostRoleGroup(\"engineering\") = %q, want %q", got, CostRoleGroupOther)
	}
}
