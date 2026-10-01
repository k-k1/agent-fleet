package awsx

import (
	"os"
	"slices"
	"strings"
)

// WorkloadOptIn hands the workspace task's own AWS identity to sessions and terminals again.
// A deployment sets it on the Control Plane, which injects it into every workspace (the ECS
// runtimes do not pass WS_ENV on). Unset, nothing the Agent starts inherits that identity.
const WorkloadOptIn = "AF_WS_WORKLOAD_AWS"

// MetadataDisabled is what the Agent puts in its own environment when it withholds the
// workload identity, and what anything rebuilding a child's environment has to keep.
const MetadataDisabled = "AWS_EC2_METADATA_DISABLED=true"

// workloadChainEnv are the variables through which ECS hands its task role to every
// process in the container. The default credential chain of the CLI and every SDK (and so
// of Gradle/Maven plugins, Terraform, CDK) falls back to them silently when no member
// credentials resolve.
var workloadChainEnv = []string{
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
}

// WorkloadChainVars returns the variable names IsolateWorkloadChain removes.
func WorkloadChainVars() []string { return slices.Clone(workloadChainEnv) }

// IsolateWorkloadChain removes the workload identity from the Agent's own environment, so
// that the sessions, terminals, managed runtimes and helpers it spawns do not inherit it: a
// build tool with no member credentials must fail instead of uploading as the workspace
// task (or, on ecs-ec2, as the slot's instance profile through IMDS). Call it before the
// Agent starts anything. It returns the variables it removed, for the boot log.
//
// This withholds automatic inheritance; it is not a boundary against a process of the same
// uid that goes looking (the values are still in /proc/1/environ). Two paths do not inherit
// the Agent's environment and are patched where they are built: a tmux server's global
// environment (tmuxx.SetLaunchEnv) and the MCP hosts that rebuild a child's environment
// from an allow-list (mcpreg.ForwardEnvNames).
//
// It acts only where ECS put the container variables there, which is how a member-owned
// machine (the native runtime, where IMDS may well be the member's own instance role) is
// left alone. AWS_EC2_METADATA_DISABLED stops the SDKs from trying IMDS; the network block
// on the slot (ECS_AWSVPC_BLOCK_IMDS in 40-ec2-pool) is what makes that hold for a tool
// that ignores the variable.
//
// The Agent itself loses nothing: it makes no AWS call with the default chain (WsTaskRole
// carries no policies), and af-aws-exec and opencode's catalog probe already scrubbed these
// for their own children.
func IsolateWorkloadChain() []string {
	if os.Getenv(WorkloadOptIn) == "1" {
		return nil
	}
	var removed []string
	for _, k := range workloadChainEnv {
		if _, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			removed = append(removed, k)
		}
	}
	if len(removed) > 0 {
		k, v, _ := strings.Cut(MetadataDisabled, "=")
		os.Setenv(k, v)
	}
	return removed
}
