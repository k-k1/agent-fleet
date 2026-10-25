package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// withWorkspaceSubnets gives the harness AF_ECS_WORKSPACE_SUBNETS. The ids are chosen so
// that sorting them pairs them with the private subnets the other way round (ws-a… is in
// 1c, ws-b… in 1a): only the AZ may pair a workspace subnet with a slot, never the order
// of either list.
func withWorkspaceSubnets(h *ec2Harness, azs map[string]string) {
	for id, az := range azs {
		h.ec2.subnetAZ[id] = az
		h.rt.base.cfg.wsSubnets = append(h.rt.base.cfg.wsSubnets, id)
	}
}

// runDeferred brings every slot launched so far up (running, registered), as EC2 and the
// ECS agent would, then runs the background half of Start.
func runDeferred(ctx context.Context, h *ec2Harness) {
	for id, inst := range h.ec2.instances {
		if strings.HasPrefix(id, "i-new") {
			inst.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
			h.ci.registered[id] = true
		}
	}
	h.runDeferred(ctx)
}

// taskSubnets is every subnet list a Create/UpdateService call gave the workspace's ENI.
func taskSubnets(h *ec2Harness) [][]string {
	var out [][]string
	for _, c := range h.ecs.createCalls {
		if c.NetworkConfiguration != nil {
			out = append(out, c.NetworkConfiguration.AwsvpcConfiguration.Subnets)
		}
	}
	for _, c := range h.ecs.updateCalls {
		if c.NetworkConfiguration != nil {
			out = append(out, c.NetworkConfiguration.AwsvpcConfiguration.Subnets)
		}
	}
	return out
}

// The task ENI goes to the workspace subnet of the slot's AZ, while the slot itself is
// launched in the private subnet: the ECS agent pulls the image through the instance's
// ENI, which keeps the pull on the private route table's S3 endpoint (#2716).
func TestECSEC2TaskENIGoesToTheWorkspaceSubnetOfTheSlotAZ(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	withWorkspaceSubnets(h, map[string]string{"ws-b1a": "ap-northeast-1a", "ws-a1c": "ap-northeast-1c"})
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runDeferred(ctx, h)

	launched := false
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "RunInstances") {
			launched = true
			if !strings.Contains(c, "subnet=sub-1a") {
				t.Errorf("the slot must stay in the private subnet of 1a: %q", c)
			}
		}
	}
	if !launched {
		t.Fatal("no slot was launched; the test does not exercise runSlot")
	}
	got := taskSubnets(h)
	if len(got) == 0 {
		t.Fatal("no service call carried a network configuration")
	}
	for _, subs := range got {
		if len(subs) != 1 || subs[0] != "ws-b1a" {
			t.Errorf("task subnets = %v, want [ws-b1a] (the workspace subnet of 1a)", subs)
		}
	}
}

// Without AF_ECS_WORKSPACE_SUBNETS nothing changes: the ENI takes the slot AZ's private
// subnet, as it always did.
func TestECSEC2TaskENIStaysInThePrivateSubnetWithoutWorkspaceSubnets(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1c")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runDeferred(ctx, h)
	got := taskSubnets(h)
	if len(got) == 0 {
		t.Fatal("no service call carried a network configuration")
	}
	for _, subs := range got {
		if len(subs) != 1 || subs[0] != "sub-1c" {
			t.Errorf("task subnets = %v, want [sub-1c]", subs)
		}
	}
}

// A home in an AZ that has no workspace subnet is refused with the setting named, never
// started on the private route table, where its S3 would hit the endpoint's policy and
// skip the egress route.
func TestECSEC2AZWithoutAWorkspaceSubnetIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	withWorkspaceSubnets(h, map[string]string{"ws-a1c": "ap-northeast-1c"})
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	err := h.rt.Start(ctx)
	runDeferred(ctx, h)
	for _, subs := range taskSubnets(h) {
		t.Errorf("a service call placed the task in %v although 1a has no workspace subnet", subs)
	}
	if err == nil || !strings.Contains(err.Error(), "AF_ECS_WORKSPACE_SUBNETS") {
		t.Errorf("Start error = %v, want one naming AF_ECS_WORKSPACE_SUBNETS", err)
	}
}

// New homes only go to AZs that have a workspace subnet; a home elsewhere could never start.
func TestECSEC2NewHomesSkipAZsWithoutAWorkspaceSubnet(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	withWorkspaceSubnets(h, map[string]string{"ws-a1c": "ap-northeast-1c"})

	azs, err := h.rt.poolAZs(ctx)
	if err != nil {
		t.Fatalf("poolAZs: %v", err)
	}
	if len(azs) != 1 || azs[0] != "ap-northeast-1c" {
		t.Errorf("poolAZs = %v, want [ap-northeast-1c]", azs)
	}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runDeferred(ctx, h)
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume: %v", err)
	}
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1c" {
		t.Errorf("the new home went to %q; only 1c has a workspace subnet", got)
	}
}

// The factory resolves the AZ of both lists in its one DescribeSubnets.
func TestECSEC2FactorySubnetAZsCoversWorkspaceSubnets(t *testing.T) {
	h := newEC2Harness(t)
	withWorkspaceSubnets(h, map[string]string{"ws-b1a": "ap-northeast-1a"})
	f := h.factory()
	m, err := f.subnetAZs(context.Background())
	if err != nil {
		t.Fatalf("subnetAZs: %v", err)
	}
	for _, id := range []string{"sub-1a", "sub-1c", "ws-b1a"} {
		if m[id] == "" {
			t.Errorf("subnetAZs has no AZ for %s: %v", id, m)
		}
	}
}

// Fargate workspaces move too: left in the private subnets, their own S3 would meet the
// endpoint's policy. An existing service is moved on its next Start, so UpdateService
// carries the configuration as well.
func TestECSFargateWorkspaceUsesWorkspaceSubnets(t *testing.T) {
	for _, existing := range []bool{false, true} {
		fe := &fakeECS{}
		if existing {
			fe.services = map[string]ecstypes.Service{
				"af-ws-acme-alice": {Status: aws.String("ACTIVE")},
			}
		}
		rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
		rt.cfg.wsSubnets = []string{"ws-1", "ws-2"}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatalf("Start (existing=%v): %v", existing, err)
		}
		var nc *ecstypes.NetworkConfiguration
		switch {
		case !existing && len(fe.createCalls) == 1:
			nc = fe.createCalls[0].NetworkConfiguration
		case existing && len(fe.updateCalls) == 1:
			nc = fe.updateCalls[0].NetworkConfiguration
		default:
			t.Fatalf("existing=%v: create/update = %d/%d", existing, len(fe.createCalls), len(fe.updateCalls))
		}
		if nc == nil {
			t.Fatalf("existing=%v: the service call carries no network configuration", existing)
		}
		if subs := nc.AwsvpcConfiguration.Subnets; strings.Join(subs, ",") != "ws-1,ws-2" {
			t.Errorf("existing=%v: subnets = %v, want [ws-1 ws-2]", existing, subs)
		}
	}
	// Unset: the private subnets, as before.
	fe := &fakeECS{}
	rt := newTestECS(fe, &fakeEFS{}, &fakeSSM{})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if subs := fe.createCalls[0].NetworkConfiguration.AwsvpcConfiguration.Subnets; strings.Join(subs, ",") != "sub-1" {
		t.Errorf("without workspace subnets: subnets = %v, want [sub-1]", subs)
	}
}
