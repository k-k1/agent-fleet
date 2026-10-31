package runtime

import (
	"slices"
	"testing"
)

func TestMemoryLimitValue(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{0, ""}, {-5, ""}, {3 << 30, "3221225472"}} {
		if got := memoryLimitValue(c.in); got != c.want {
			t.Errorf("memoryLimitValue(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDockerMemoryLimitArgs(t *testing.T) {
	for _, c := range []struct {
		memory string
		want   []string
	}{
		{"4g", []string{"-e", MemoryLimitEnv + "=4294967296"}},
		{"512m", []string{"-e", MemoryLimitEnv + "=536870912"}},
		{"1073741824", []string{"-e", MemoryLimitEnv + "=1073741824"}},
		{"", nil},
		{"lots", nil},
	} {
		d := &dockerRuntime{memory: c.memory}
		if got := d.memoryLimitArgs(); !slices.Equal(got, c.want) {
			t.Errorf("memory %q: args = %v, want %v", c.memory, got, c.want)
		}
	}
}
