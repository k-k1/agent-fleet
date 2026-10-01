package awsx

import "os"

// WorkloadOptIn is the deployment switch (the Control Plane's WS_ENV) that hands the
// workspace task's own AWS identity to sessions and terminals again. Unset, nothing the
// Agent starts can reach it.
const WorkloadOptIn = "AF_WS_WORKLOAD_AWS"

// workloadChainEnv are the variables through which ECS hands its task role to every
// process in the container. The default credential chain of the CLI and every SDK (and so
// of Gradle/Maven plugins, Terraform, CDK) falls back to them silently when no member
// credentials resolve.
var workloadChainEnv = []string{
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
}

// IsolateWorkloadChain removes the workload identity from the Agent's own environment, so
// that no session, terminal, managed runtime or helper it spawns inherits it: a build tool
// with no member credentials must fail instead of uploading as the workspace task (or, on
// ecs-ec2, as the slot's instance profile through IMDS). Call it before the Agent starts
// anything. It returns the variables it removed, for the boot log.
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
		os.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	}
	return removed
}
