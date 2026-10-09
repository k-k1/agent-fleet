package runtime

import "testing"

// Only kubernetes and Fargate withhold browser features; every other adapter must keep
// launching Chromium as before, so none of them may grow the method by accident. ecs-ec2
// wraps the Fargate adapter and must not inherit its answer.
func TestBrowserUnavailablePerRuntime(t *testing.T) {
	for _, c := range []struct {
		name string
		rt   Runtime
		want string
	}{
		{"kubernetes", &kubeRuntime{}, "kubernetes"},
		{"docker", &dockerRuntime{}, ""},
		{"ecs", &ecsRuntime{}, "ecs"},
		{"ecs-ec2", &ecsEC2Runtime{}, ""},
		{"ecs-ec2 wrapping ecs", &ecsEC2Runtime{base: &ecsRuntime{}}, ""},
		{"native", &nativeRuntime{}, ""},
	} {
		if got := BrowserUnavailable(c.rt); got != c.want {
			t.Errorf("%s: BrowserUnavailable = %q, want %q", c.name, got, c.want)
		}
	}
}
