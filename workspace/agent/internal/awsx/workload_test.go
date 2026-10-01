package awsx

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The four variables ECS can hand a task its role through, spelled out here rather than
// read from workloadChainEnv: dropping one from the production list must turn a case red.
var ecsCredentialVars = []string{
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
}

// clearWorkload makes the test independent of the machine it runs on (a CI runner or a
// workspace on ECS has some of these set) and restores them afterwards.
func clearWorkload(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{WorkloadOptIn, "AWS_EC2_METADATA_DISABLED"}, ecsCredentialVars...) {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// spawnedEnv reports, for the named variables only, what a process the Agent spawns with
// the inherited environment sees — the path every session, terminal and helper takes. Only
// these names are read back, so a failure cannot print anything else from the environment.
func spawnedEnv(t *testing.T, names ...string) map[string]string {
	t.Helper()
	script := ""
	for _, n := range names {
		script += `if [ "${` + n + `+x}" ]; then printf '%s=%s\n' ` + n + ` "$` + n + `"; fi;`
	}
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

func TestIsolateWorkloadChainWithholdsEachVariable(t *testing.T) {
	for _, k := range ecsCredentialVars {
		t.Run(k, func(t *testing.T) {
			clearWorkload(t)
			t.Setenv(k, "fake")

			if removed := IsolateWorkloadChain(); len(removed) != 1 || removed[0] != k {
				t.Errorf("removed = %v, want [%s]", removed, k)
			}
			env := spawnedEnv(t, k, "AWS_EC2_METADATA_DISABLED")
			if _, ok := env[k]; ok {
				t.Errorf("child still inherits %s", k)
			}
			// IMDS is not a variable: on ecs-ec2 the slot's instance profile is the next link
			// in the chain once the container variables are gone.
			if env["AWS_EC2_METADATA_DISABLED"] != "true" {
				t.Errorf("child may still fall back to IMDS (AWS_EC2_METADATA_DISABLED=%q)", env["AWS_EC2_METADATA_DISABLED"])
			}
		})
	}
}

func TestIsolateWorkloadChainOptInKeepsEachVariable(t *testing.T) {
	for _, k := range ecsCredentialVars {
		t.Run(k, func(t *testing.T) {
			clearWorkload(t)
			t.Setenv(k, "fake")
			t.Setenv(WorkloadOptIn, "1")

			if removed := IsolateWorkloadChain(); removed != nil {
				t.Errorf("opt-in still removed %v", removed)
			}
			env := spawnedEnv(t, k, "AWS_EC2_METADATA_DISABLED")
			if env[k] != "fake" {
				t.Errorf("opt-in did not keep %s", k)
			}
			if _, ok := env["AWS_EC2_METADATA_DISABLED"]; ok {
				t.Error("opt-in disabled IMDS")
			}
		})
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

// On docker there is no container variable to remove; the Control Plane starts the
// container with AWS_EC2_METADATA_DISABLED=true, and that alone has to arm the paths that
// do not inherit the Agent's environment. Native (no such variable) and the opt-in do not.
func TestIsolationActive(t *testing.T) {
	cases := []struct {
		name           string
		imdsOff, optIn string
		want           bool
	}{
		{"docker: set by the CP", "true", "", true},
		{"native: nothing set", "", "", false},
		{"opted in", "true", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearWorkload(t)
			if tc.imdsOff != "" {
				t.Setenv("AWS_EC2_METADATA_DISABLED", tc.imdsOff)
			}
			if tc.optIn != "" {
				t.Setenv(WorkloadOptIn, tc.optIn)
			}
			if removed := IsolateWorkloadChain(); removed != nil {
				t.Fatalf("removed %v with no container variable", removed)
			}
			if got := IsolationActive(); got != tc.want {
				t.Errorf("IsolationActive() = %v, want %v", got, tc.want)
			}
		})
	}
}
