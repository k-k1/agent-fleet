package awsx

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// clearWorkload makes the test independent of the machine it runs on (a CI runner or a
// workspace on ECS has some of these set) and restores them afterwards.
func clearWorkload(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{WorkloadOptIn, "AWS_EC2_METADATA_DISABLED"}, workloadChainEnv...) {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// spawnedEnv is what a process the Agent spawns with the inherited environment sees — the
// path every session, terminal and helper takes.
func spawnedEnv(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestIsolateWorkloadChainWithholdsTaskRoleFromChildren(t *testing.T) {
	clearWorkload(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/v2/credentials/x")
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "tok")

	removed := IsolateWorkloadChain()

	if strings.Join(removed, ",") != "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI,AWS_CONTAINER_AUTHORIZATION_TOKEN" {
		t.Errorf("removed = %v", removed)
	}
	env := spawnedEnv(t)
	for _, k := range workloadChainEnv {
		if strings.Contains(env, k+"=") {
			t.Errorf("child still inherits %s", k)
		}
	}
	// IMDS is not a variable: on ecs-ec2 the slot's instance profile is the next link in
	// the chain once the container variables are gone.
	if !strings.Contains(env, "AWS_EC2_METADATA_DISABLED=true\n") {
		t.Errorf("child may still fall back to IMDS:\n%s", env)
	}
}

func TestIsolateWorkloadChainOptIn(t *testing.T) {
	clearWorkload(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://169.254.170.23/v1/credentials")
	t.Setenv(WorkloadOptIn, "1")

	if removed := IsolateWorkloadChain(); removed != nil {
		t.Errorf("opt-in still removed %v", removed)
	}
	env := spawnedEnv(t)
	if !strings.Contains(env, "AWS_CONTAINER_CREDENTIALS_FULL_URI=") || strings.Contains(env, "AWS_EC2_METADATA_DISABLED=") {
		t.Errorf("opt-in did not keep the chain:\n%s", env)
	}
}

// Off ECS there is no workload variable, and IMDS may be the member's own instance role
// (the native runtime on their EC2 box): nothing changes.
func TestIsolateWorkloadChainOffECSIsNoop(t *testing.T) {
	clearWorkload(t)
	if removed := IsolateWorkloadChain(); removed != nil {
		t.Errorf("removed = %v", removed)
	}
	if _, ok := os.LookupEnv("AWS_EC2_METADATA_DISABLED"); ok {
		t.Error("IMDS disabled on a machine ECS did not set up")
	}
}
