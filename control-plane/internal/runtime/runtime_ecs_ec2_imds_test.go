package runtime

import (
	"os"
	"regexp"
	"testing"
)

// A workspace task is awsvpc, and an awsvpc task's ENI reaches IMDS whatever the hop limit
// is, so without ECS_AWSVPC_BLOCK_IMDS every agent shell on a slot can take SlotRole's
// credentials. The ECS agent reads the setting from ecs.config, which the slot's user data
// writes: pin it there.
func TestSlotUserDataBlocksIMDSForTasks(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ecsConfig := regexp.MustCompile(`(?s)cat >> /etc/ecs/ecs\.config <<EOF\n(.*?)\n\s*EOF\n`).FindSubmatch(b)
	if ecsConfig == nil {
		t.Fatal("no ecs.config heredoc in the slot user data")
	}
	if !regexp.MustCompile(`(?m)^\s*ECS_AWSVPC_BLOCK_IMDS=true\s*$`).Match(ecsConfig[1]) {
		t.Errorf("slot ecs.config does not set ECS_AWSVPC_BLOCK_IMDS=true:\n%s", ecsConfig[1])
	}
}

// The compose host runs workspaces on docker bridges next to a host-network Control Plane.
// Hop limit 1 is what keeps a bridge container (two hops) off an instance profile the CP
// (one hop) may be given; IMDSv1 has no hop limit at all, so tokens must be required.
func TestSingleVMBlocksIMDSForBridgeContainers(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ec2-single/cfn.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s+MetadataOptions:\s*\{\s*HttpTokens:\s*required,\s*HttpPutResponseHopLimit:\s*1\s*\}`).Match(b) {
		t.Error("ec2-single instance must declare MetadataOptions { HttpTokens: required, HttpPutResponseHopLimit: 1 }")
	}
}
