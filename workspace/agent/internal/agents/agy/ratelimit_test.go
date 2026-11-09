package agy

import (
	"testing"
	"time"
)

func TestLimitInMessageReadsTheReset(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"error: Individual quota reached. Resets in 108h50m38s.": 108*time.Hour + 50*time.Minute + 38*time.Second,
		"Individual quota reached. Resets in 1d2h.":              26 * time.Hour,
		"Individual quota reached. Resets in 108h 50m.":          108*time.Hour + 50*time.Minute,
		"Individual quota reached. RESETS IN 108H.":              108 * time.Hour,
		"Individual quota reached. Resets in 45m.":               45 * time.Minute,
		"Individual quota reached.":                              0,
	}
	for msg, want := range cases {
		at, ok := LimitInMessage(msg, now)
		if !ok {
			t.Errorf("%q: not recognised", msg)
			continue
		}
		if got := at.Sub(now); (want == 0 && !at.IsZero()) || (want != 0 && got != want) {
			t.Errorf("%q: reset in %v, want %v", msg, got, want)
		}
	}
	if _, ok := LimitInMessage("RESOURCE_EXHAUSTED (code 429): too many requests", now); ok {
		t.Error("a plain 429 is not the quota refusal")
	}
}
