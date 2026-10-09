package runtime

import (
	"os"
	"regexp"
	"strings"
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

// The ecs-ec2 task adds SYS_ADMIN for Chromium's setuid sandbox, so the slot must close
// unprivileged user namespaces, and must do so before the ECS agent can start a task
// (the sysctl comes ahead of the ecs.config write), and the home must be mounted
// nosuid,nodev. The CP holds no copy of the mount options (it only calls af-mount), so
// this is the one place they are pinned.
func TestSlotUserDataHardensForSysAdmin(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/aws/ecs/cfn/40-ec2-pool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ud := string(b)
	persist := strings.Index(ud, "/etc/sysctl.d/99-af-userns.conf")
	apply := strings.Index(ud, "sysctl -w user.max_user_namespaces=0")
	ecsCfg := strings.Index(ud, "cat >> /etc/ecs/ecs.config")
	if persist < 0 || apply < 0 || !strings.Contains(ud, "user.max_user_namespaces=0' > /etc/sysctl.d/") {
		t.Fatal("slot user data does not persist and apply user.max_user_namespaces=0")
	}
	if ecsCfg < 0 || apply > ecsCfg {
		t.Error("the userns sysctl must be applied before the ECS agent is configured")
	}
	if !regexp.MustCompile(`mount -o nouuid,nosuid,nodev "\$DEV" "\$MP"`).MatchString(ud) {
		t.Error("af-mount must mount the home with nouuid,nosuid,nodev")
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
