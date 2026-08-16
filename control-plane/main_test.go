// main_test.go — main.go の小さな純関数（env 解決）のテスト。
package main

import (
	"testing"
	"time"
)

// "0 disables it" has to actually disable it. parseDurationOr treats every non-positive
// value as "not set" and hands back the default, which is right for a timeout (0 means
// "no timeout configured") and wrong for a sweep interval (0 means "do not sweep").
func TestIntervalOffHonoursAnExplicitZero(t *testing.T) {
	cases := []struct {
		in   string
		def  time.Duration
		want time.Duration
	}{
		{"", time.Minute, time.Minute},           // unset → default
		{"0", time.Minute, 0},                    // explicitly off
		{"0s", time.Minute, 0},                   // same, spelled out
		{" 30s ", time.Minute, 30 * time.Second}, // set
		{"never", time.Minute, time.Minute},      // garbage must NOT turn a sweep off
	}
	for _, tc := range cases {
		if got := intervalOff(tc.in, tc.def); got != tc.want {
			t.Errorf("intervalOff(%q, %s) = %s, want %s", tc.in, tc.def, got, tc.want)
		}
	}
}
