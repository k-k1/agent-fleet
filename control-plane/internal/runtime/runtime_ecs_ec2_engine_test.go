package runtime

// Where the slot pool and the engines share a cluster and a pool tag (ADR 0077 decision 3).
//
// The engines are not this package's business — the CP buys their boxes, runs them and ends them
// (engine_fleet.go) — but they land in the SAME ECS cluster and carry the SAME `af-pool` tag, so
// every walk here has to be able to say "not mine". Both halves of that are tested with their
// positive control, because a filter that excludes everything and a filter that excludes the
// right thing look identical from a passing test.

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// containerInstance builds one cluster member the way ECS reports it: a capacity provider name
// (a Managed Instances box), an `af-role` attribute (a box the CP bought with EC2 Fleet), or
// neither (a workspace slot).
func containerInstance(provider, role string) ecstypes.ContainerInstance {
	ci := ecstypes.ContainerInstance{
		ContainerInstanceArn: aws.String("arn:ci/x"), Ec2InstanceId: aws.String("i-x"),
		Status: aws.String("ACTIVE"), AgentConnected: true,
		Attributes: []ecstypes.Attribute{
			{Name: aws.String("ecs.availability-zone"), Value: aws.String("ap-northeast-1a")},
		},
	}
	if provider != "" {
		ci.CapacityProviderName = aws.String(provider)
	}
	if role != "" {
		ci.Attributes = append(ci.Attributes, ecstypes.Attribute{
			Name: aws.String(EC2TagRole), Value: aws.String(role),
		})
	}
	return ci
}

// 🔴 ADR 0077 done item 4. Every container-instance walk in this file is written for pool members
// — "ACTIVE and agentConnected means a slot is ready", "no task means the box is idle", "the EC2
// instance is gone so deregister" — and an engine box satisfies none of those premises. THREE
// kinds have to sort correctly, and each is excluded by a different half of the test:
//
//   - a Managed Instances engine box (ADR 0071) carries its provider's NAME and no attribute;
//   - a box the CP bought with EC2 Fleet (ADR 0077) carries an EMPTY provider name — which is
//     why the provider test alone collapses — and the `af-role` attribute its user data wrote;
//   - a slot carries neither, and an old slot from before the attribute existed carries neither
//     either, so "absent" has to mean "slot".
func TestIsPoolContainerInstanceSortsTheThreeKindsOfBox(t *testing.T) {
	for _, tc := range []struct {
		name             string
		provider, role   string
		wantPoolInstance bool
	}{
		{"a workspace slot", "", "", true},
		{"a slot that spells its role out", "", ec2RoleSlot, true},
		{"a quarantined slot is still the pool's", "", ec2RoleQuarantined, false},
		{"a Managed Instances engine box", "af-stack-image", "", false},
		{"an EC2 Fleet engine box", "", "engine-image", false},
		{"the other role's EC2 Fleet box", "", "engine-llm", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPoolContainerInstance(containerInstance(tc.provider, tc.role)); got != tc.wantPoolInstance {
				t.Errorf("isPoolContainerInstance = %v, want %v", got, tc.wantPoolInstance)
			}
		})
	}

	// 🔴 The positive control for the attribute half: the SAME box without the attribute is read
	// as a slot. Without this, a test of the engine cases would pass against an implementation
	// that simply refused everything.
	if !isPoolContainerInstance(containerInstance("", "")) {
		t.Fatal("the attribute-less box is not a pool member either — the cases above prove nothing")
	}
	// And for the provider half: dropping the provider test would let a Managed Instances box —
	// which carries no attribute — back into the pool. Both halves are needed until decision 11's
	// migration has been run on every deployment.
	if isPoolContainerInstance(containerInstance("af-stack-image", "")) {
		t.Fatal("a Managed Instances box counted as a slot")
	}
}

// 🔴 ADR 0077 done item 5, and the defect review R3 found before a line was written: five
// EC2-side walks pair `af-pool` with `af-role=slot`, and this one filtered on the pool ALONE. It
// then writes or strips `af-membership` / `af-tenant` on whatever it finds — so an engine box,
// which carries `af-pool` too (decision 3), would be relabelled by it and somebody's hours would
// be billed to a GPU they never had. Nothing in the pool logic reads those tags, so the only
// place it would ever show is the invoice.
func TestSweepSlotOwnerTagsLeavesAnEngineBoxAlone(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)

	// An engine box in the same pool, wrongly carrying a person's tags. Nothing here is holding
	// a home, so the un-filtered sweep would strip them — a write on a box it does not own.
	engine := h.ec2.addSlot("i-engine", "ap-northeast-1a", "g6.xlarge", true, false)
	engine.Tags = []ec2types.Tag{
		ec2Tag(EC2TagPool, "clu"), ec2Tag(EC2TagRole, "engine-image"),
		ec2Tag(EC2TagMembership, "M-1"), ec2Tag(EC2TagTenant, "acme"),
	}
	// A real slot in the same state, which IS the sweep's business.
	h.ec2.addSlot("i-slot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.instances["i-slot"].Tags = append(h.ec2.instances["i-slot"].Tags,
		ec2Tag(EC2TagMembership, "M-2"), ec2Tag(EC2TagTenant, "acme"))

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := instTag(h, "i-engine", EC2TagMembership); got != "M-1" {
		t.Errorf("the engine box's af-membership is now %q — the sweep wrote to a box it does not own", got)
	}
	if got := instTag(h, "i-engine", EC2TagTenant); got != "acme" {
		t.Errorf("the engine box's af-tenant is now %q", got)
	}
	// The positive control, and the reason the assertion above is about the filter: the slot next
	// to it, in the identical state, was repaired.
	if got := instTag(h, "i-slot", EC2TagMembership); got != "" {
		t.Errorf("the slot still bills %q — the sweep did not run at all, so this test proves nothing", got)
	}
}
