package mcpx

import "testing"

func TestWithEnvReplacesInheritedNames(t *testing.T) {
	base := []string{"PATH=/bin", "GRAFANA_USAGE_STATS=enabled", "DO_NOT_TRACK=0", "GRAFANA_USAGE_STATS=log"}
	got := withEnv(base, "GRAFANA_USAGE_STATS=disabled", "DO_NOT_TRACK=1")
	want := []string{"PATH=/bin", "GRAFANA_USAGE_STATS=disabled", "DO_NOT_TRACK=1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
