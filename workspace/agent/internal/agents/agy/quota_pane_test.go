package agy

import (
	"testing"
	"time"
)

const (
	quotaLine  = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h39m46s."
	idleChrome = "\n─────────────\n> \n─────────────\n? for shortcuts      Gemini · high\n"
)

func TestQuotaBannerInFrame(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		want        bool
	}{
		{"refusal after a tool call and partial output",
			"> do it\nrunning ls\nPartial answer so" + "\n" + quotaLine + idleChrome, true},
		{"refusal wrapped over two lines",
			"Individual quota reached. Please upgrade your subscription to\nincrease your limits. Resets in 1h39m46s." + idleChrome, true},
		{"no countdown",
			"Individual quota reached. Please upgrade your subscription to increase your limits." + idleChrome, true},
		{"newer successful turn below an old refusal",
			quotaLine + "\n> next\nDone, here is the answer." + idleChrome, false},
		{"newer one-line reply right below the refusal",
			quotaLine + "\nOK" + idleChrome, false},
		{"newer turn still running",
			quotaLine + "\n> next\nthinking\n─────\n> \n─────\n  esc to cancel      Gemini · high\n", false},
		{"no banner", "hello\nworld" + idleChrome, false},
		{"older refusal then a newer refusal",
			quotaLine + "\n> again\n" + quotaLine + idleChrome, true},
	} {
		if _, got := quotaBannerInFrame(tc.frame); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// The countdown read off the banner is the one the caller turns into a reset instant.
func TestQuotaBannerCarriesTheCountdown(t *testing.T) {
	text, ok := quotaBannerInFrame("x\n" + quotaLine + idleChrome)
	if !ok {
		t.Fatal("banner not found")
	}
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	at, ok := LimitInMessage(text, base)
	if want := base.Add(time.Hour + 39*time.Minute + 46*time.Second); !ok || !at.Equal(want) {
		t.Fatalf("reset %v ok=%v, want %v", at, ok, want)
	}
}

// agy is a usage-limit kind for Terminal sessions too: the sweep must not skip it.
func TestAgyUsageLimitTraitsAreWired(t *testing.T) {
	a := agentImpl{}
	tr := a.UsageLimitTraits()
	if tr.AtLimit == nil || tr.ResetAt == nil || tr.ManagedOnly {
		t.Fatalf("traits not wired for Terminal: %+v", tr)
	}
	if a.LiveStateTraits().TurnFailure == nil {
		t.Fatal("polled turn end has no failure reader")
	}
}
