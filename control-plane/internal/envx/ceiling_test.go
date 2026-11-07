package envx

import "testing"

func TestEnvCeiling(t *testing.T) {
	for _, c := range []struct {
		val  string
		set  bool
		want int
	}{{"", false, 16384}, {"  ", true, 16384}, {"4096", true, 4096}, {"0", true, 0}, {"-1", true, 16384}, {"8 vCPU", true, 16384}} {
		if c.set {
			t.Setenv("AF_TEST_CEILING", c.val)
		} else {
			t.Setenv("AF_TEST_CEILING", "")
		}
		if got := Ceiling("AF_TEST_CEILING", 16384); got != c.want {
			t.Errorf("%q: got %d, want %d", c.val, got, c.want)
		}
	}
}
