package hostcaps

import "testing"

func TestRdrandInCPUInfo(t *testing.T) {
	// 実機の /proc/cpuinfo の形（キーはタブ詰め、flags は空白区切りの語集合）。
	withRdrand := `processor	: 0
vendor_id	: AuthenticAMD
model name	: AMD Ryzen 7 PRO 8840HS w/ Radeon 780M Graphics
flags		: fpu vme de pse tsc msr pae mce cx8 rdrand rdseed clflushopt sha_ni
bugs		: sysret_ss_attrs spectre_v1 spectre_v2
`
	// RDRAND 非提示ホスト（0008 の Ryzen Embedded R2514 相当）: flags に rdrand が無い。
	withoutRdrand := `processor	: 0
vendor_id	: AuthenticAMD
model name	: AMD Ryzen Embedded R2514 with Radeon Vega Graphics
flags		: fpu vme de pse tsc msr pae mce cx8 sse4_1 sse4_2 aes xsave
`
	// 語境界: flags の語としての rdrand だけを認める（部分一致や他行の混入は不可）。
	tricky := `model name	: fake rdrand cpu
flags		: fpu srdrandx rdrand2 rdseed
`
	// arm64 等: flags 行自体が無い（Features 行）。x86 要件の判定なので false でよい。
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
	// 実ホスト依存のスモーク: 返り値が契約の語彙に収まっていること
	// （docs/log/32 — Console は supported=false の kind をセレクタに出さない）。
	supported, reason := AgyStatus()
	switch {
	case supported && reason != "":
		t.Errorf("supported=true must carry empty reason, got %q", reason)
	case !supported && reason != "not_installed" && reason != "no_rdrand":
		t.Errorf("unsupported reason %q outside contract vocabulary", reason)
	}
}
