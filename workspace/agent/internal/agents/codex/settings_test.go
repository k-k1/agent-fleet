package codex

import (
	"strings"
	"testing"
)

func TestRateLimitModelNudgeDefaultAndRoundTrip(t *testing.T) {
	original := []byte("model = \"gpt-5.6-sol\"\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	if !rateLimitModelNudgeEnabled(original) {
		t.Fatal("absent key should default to enabled")
	}
	off := setRateLimitModelNudge(original, false)
	if rateLimitModelNudgeEnabled(off) {
		t.Fatal("disabled setting was not read back")
	}
	if !strings.Contains(string(off), "[notice]\nhide_rate_limit_model_nudge = true") ||
		!strings.Contains(string(off), "[projects.\"/repo\"]\ntrust_level = \"trusted\"") {
		t.Fatalf("unrelated config was not preserved:\n%s", off)
	}
	on := setRateLimitModelNudge(off, true)
	if !rateLimitModelNudgeEnabled(on) || strings.Count(string(on), "hide_rate_limit_model_nudge") != 1 {
		t.Fatalf("enabled setting did not round-trip cleanly:\n%s", on)
	}
}

func TestSetRateLimitModelNudgeInsideExistingNotice(t *testing.T) {
	original := []byte("[notice]\nhide_full_access_warning = true\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	got := string(setRateLimitModelNudge(original, false))
	want := "[notice]\nhide_full_access_warning = true\n\nhide_rate_limit_model_nudge = true\n[projects.\"/repo\"]"
	if !strings.Contains(got, want) {
		t.Fatalf("notice entry inserted in wrong section:\n%s", got)
	}
}
