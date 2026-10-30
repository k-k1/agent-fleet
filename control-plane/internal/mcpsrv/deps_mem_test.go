package mcpsrv

import "testing"

// formatMemHuman renders the effective memory in set_user_quota's audit detail.
func TestFormatMemHuman(t *testing.T) {
	cases := map[int64]string{
		8 * gib:    "8g",
		512 * mib:  "512m",
		1536 * mib: "1536m",
		1024:       "1k",
		1000:       "1000",
	}
	for in, want := range cases {
		if got := formatMemHuman(in); got != want {
			t.Errorf("formatMemHuman(%d) = %q; want %q", in, got, want)
		}
	}
}
