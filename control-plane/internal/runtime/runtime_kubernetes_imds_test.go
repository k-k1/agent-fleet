package runtime

import (
	"testing"
	"time"
)

// The kubernetes workspace gets the same IMDS opt-out docker gives it, with the same
// precedence: an operator's WS_ENV overrides it, and AF_WS_WORKLOAD_AWS=1 leaves it out.
func TestKubeSecretEnvDisablesIMDS(t *testing.T) {
	cases := []struct {
		name     string
		template []string
		extra    []string
		want     string
		present  bool
	}{
		{"default", nil, nil, "true", true},
		{"operator WS_ENV overrides", []string{"AWS_EC2_METADATA_DISABLED=false"}, nil, "false", true},
		{"start env overrides", nil, []string{"AWS_EC2_METADATA_DISABLED=false"}, "false", true},
		{"workload AWS opt-in leaves the chain alone", nil, []string{"AF_WS_WORKLOAD_AWS=1"}, "", false},
	}
	for _, c := range cases {
		f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1", templateEnv: c.template}}
		rt := f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, c.extra).(*kubeRuntime)
		got, ok := rt.secretEnv()["AWS_EC2_METADATA_DISABLED"]
		if got != c.want || ok != c.present {
			t.Errorf("%s: AWS_EC2_METADATA_DISABLED = %q (present=%v), want %q (present=%v)", c.name, got, ok, c.want, c.present)
		}
	}
}

// The variable lives in the Secret, not the pod template: a container `env` entry would beat
// envFrom and take away the operator's override.
func TestKubePodTemplateDoesNotPinIMDS(t *testing.T) {
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1"}}
	rt := f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, nil).(*kubeRuntime)
	for _, e := range rt.podTemplate("img:1", 1, time.Unix(0, 0)).Spec.Containers[0].Env {
		if e.Name == "AWS_EC2_METADATA_DISABLED" {
			t.Errorf("pod template env pins %s", e.Name)
		}
	}
}

func TestKubeRuntimeOwnsItsHome(t *testing.T) {
	f := &kubeFactory{cfg: &kubeConfig{namespace: "ns", image: "img:1"}}
	rt := f.New(Workspace{ContainerName: "af-ws-x"}, SecretKeys{}, nil).(*kubeRuntime)
	if !rt.OwnHomeVolume() {
		t.Error("a per-workspace home claim must report an own home volume")
	}
}
