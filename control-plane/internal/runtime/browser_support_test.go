package runtime

import "testing"

// Only the kubernetes runtime withholds browser features; every other adapter must keep
// launching Chromium as before, so none of them may grow the method by accident.
func TestBrowserUnavailableIsKubernetesOnly(t *testing.T) {
	for _, c := range []struct {
		name string
		rt   Runtime
		want string
	}{
		{"kubernetes", &kubeRuntime{}, "kubernetes"},
		{"docker", &dockerRuntime{}, ""},
		{"ecs", &ecsRuntime{}, ""},
		{"ecs-ec2", &ecsEC2Runtime{}, ""},
		{"native", &nativeRuntime{}, ""},
	} {
		if got := BrowserUnavailable(c.rt); got != c.want {
			t.Errorf("%s: BrowserUnavailable = %q, want %q", c.name, got, c.want)
		}
	}
}
