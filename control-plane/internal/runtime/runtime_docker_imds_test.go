package runtime

import (
	"slices"
	"testing"
)

// A docker workspace on an EC2 host reaches the host's instance metadata service whenever
// the hop limit allows it, so the SDKs in every session must be told not to ask — unless
// the deployment opted in to the workload identity, as on ECS.
func TestDockerWorkspaceEnvDisablesIMDS(t *testing.T) {
	has := func(args []string, kv string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-e" && args[i+1] == kv {
				return true
			}
		}
		return false
	}
	d := &dockerRuntime{extraEnv: []string{"FOO=1"}}
	if args := d.envArgs(); !has(args, "AWS_EC2_METADATA_DISABLED=true") || !has(args, "FOO=1") {
		t.Errorf("default workspace env = %v", args)
	}
	d = &dockerRuntime{extraEnv: []string{"AF_WS_WORKLOAD_AWS=1"}}
	if args := d.envArgs(); slices.Contains(args, "AWS_EC2_METADATA_DISABLED=true") {
		t.Errorf("opted-in workspace still disables IMDS: %v", args)
	}
	// An operator's own value comes later on the command line, and docker keeps the last.
	d = &dockerRuntime{extraEnv: []string{"AWS_EC2_METADATA_DISABLED=false"}}
	args := d.envArgs()
	if i, j := slices.Index(args, "AWS_EC2_METADATA_DISABLED=true"), slices.Index(args, "AWS_EC2_METADATA_DISABLED=false"); i < 0 || j < i {
		t.Errorf("WS_ENV cannot override the default: %v", args)
	}
}
