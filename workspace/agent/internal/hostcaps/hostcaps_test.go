package hostcaps

import "testing"

func TestRdrandInCPUInfo(t *testing.T) {
	// The shape of a real /proc/cpuinfo (keys padded with tabs, flags a space-separated
	// set of words).
	withRdrand := `processor	: 0
vendor_id	: AuthenticAMD
model name	: AMD Ryzen 7 PRO 8840HS w/ Radeon 780M Graphics
flags		: fpu vme de pse tsc msr pae mce cx8 rdrand rdseed clflushopt sha_ni
bugs		: sysret_ss_attrs spectre_v1 spectre_v2
`
	// A host that does not advertise RDRAND (0008's Ryzen Embedded R2514): no rdrand in flags.
	withoutRdrand := `processor	: 0
vendor_id	: AuthenticAMD
model name	: AMD Ryzen Embedded R2514 with Radeon Vega Graphics
flags		: fpu vme de pse tsc msr pae mce cx8 sse4_1 sse4_2 aes xsave
`
	// Word boundary: only rdrand as a word in flags counts (no substring match, and no
	// other line).
	tricky := `model name	: fake rdrand cpu
flags		: fpu srdrandx rdrand2 rdseed
`
	// arm64 and the like: there is no flags line at all (it is Features). This decides an
	// x86 requirement, so false is the right answer.
	arm := `processor	: 0
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 rdrand
`
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"rdrand_present", withRdrand, true},
		{"rdrand_absent", withoutRdrand, false},
		{"word_boundary_only", tricky, false},
		{"no_flags_line_arm", arm, false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rdrandInCPUInfo(tc.in); got != tc.want {
				t.Errorf("rdrandInCPUInfo(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestAgyStatusReasonVocabulary(t *testing.T) {
	// Smoke test against the real host: the return values must stay inside the contract's
	// vocabulary (docs/log/32 — the Console keeps kinds with supported=false out of the
	// selector).
	supported, reason := AgyStatus()
	switch {
	case supported && reason != "":
		t.Errorf("supported=true must carry empty reason, got %q", reason)
	case !supported && reason != "not_installed" && reason != "no_rdrand":
		t.Errorf("unsupported reason %q outside contract vocabulary", reason)
	}
}
