package agy

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
		{"echo of the next prompt below an old refusal",
			quotaLine + "\n> next\n> \n─────\n? for shortcuts      Gemini · high\n", false},
		{"model quoting the phrase",
			"The error is called Individual quota reached." + idleChrome, false},
		{"model quoting the phrase with the hint",
			"Sure: the message says Individual quota reached. Please upgrade your subscription to increase your limits." + idleChrome, false},
		{"error label and glyph before the banner",
			"✗ error: " + quotaLine + idleChrome, true},
		{"wrap splits a number",
			"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h3\n9m46s." + idleChrome, true},
		{"wrap between Individual and quota",
			"Individual\nquota reached. Please upgrade your subscription to increase your limits." + idleChrome, true},
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
	if tr.AtLimit == nil || tr.ResetAt == nil || tr.RanTurnSince == nil || tr.ManagedOnly {
		t.Fatalf("traits not wired for Terminal: %+v", tr)
	}
	if lt := a.LiveStateTraits(); lt.TurnFailure == nil || !lt.TurnFailureUnmarked {
		t.Fatal("polled turn end has no failure reader")
	}
}

// Only the newest banner is read: an older countdown on screen never leaks into the reset.
func TestQuotaBannerIsOnlyTheNewestOne(t *testing.T) {
	old := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 25m."
	text, ok := quotaBannerInFrame(old + "\n> again\npartial\n" + quotaLine + idleChrome)
	if !ok || text != quotaLine {
		t.Fatalf("got %q ok=%v, want only the newest banner", text, ok)
	}
	noCountdown := "Individual quota reached. Please upgrade your subscription to increase your limits."
	text, ok = quotaBannerInFrame(old + "\n> again\n" + noCountdown + idleChrome)
	if _, found := LimitInMessage(text, time.Now()); !ok || !found {
		t.Fatalf("banner %q ok=%v", text, ok)
	}
	if at, _ := LimitInMessage(text, time.Now()); !at.IsZero() {
		t.Fatalf("the older 25m countdown was read: %v", at)
	}
}

// The reset counts from the banner's first sighting, and stays put while the same banner stays.
func TestQuotaResetCountsFromFirstSighting(t *testing.T) {
	old := capturePaneFrame
	defer func() { capturePaneFrame = old }()
	capturePaneFrame = func(string) string { return "x\n" + quotaLine + idleChrome }
	m := session.Meta{Name: "qr1", Dir: t.TempDir()}
	defer forgetQuota(m.Name)
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	at, src, ok := quotaResetAt(m, false, t0)
	want := t0.Add(time.Hour + 39*time.Minute + 46*time.Second)
	if !ok || !at.Equal(want) || src != quotaRefusalSource {
		t.Fatalf("got %v %q %v, want %v", at, src, ok, want)
	}
	if at2, _, _ := quotaResetAt(m, false, t0.Add(10*time.Minute)); !at2.Equal(want) {
		t.Fatalf("second poll moved the reset to %v", at2)
	}
	// The banner leaves the tail, then a new refusal with the same text counts from its own sighting.
	capturePaneFrame = func(string) string { return "ok" + idleChrome }
	if _, atLimit := atQuotaLimit(m); atLimit {
		t.Fatal("still at limit without a banner")
	}
	capturePaneFrame = func(string) string { return "x\n" + quotaLine + idleChrome }
	t1 := t0.Add(3 * time.Hour)
	if at3, _, _ := quotaResetAt(m, false, t1); !at3.Equal(t1.Add(time.Hour + 39*time.Minute + 46*time.Second)) {
		t.Fatalf("new refusal reset %v", at3)
	}
}

// A capture that fails once, or a footer not painted yet, must not end as a clean completion.
func TestTurnFailureRetriesAnUnreadablePane(t *testing.T) {
	old, oldWait := capturePaneFrame, quotaRetryWait
	defer func() { capturePaneFrame, quotaRetryWait = old, oldWait }()
	quotaRetryWait = 0
	m := session.Meta{Name: "qr2", Dir: t.TempDir()}
	defer forgetQuota(m.Name)
	calls := 0
	capturePaneFrame = func(string) string {
		calls++
		switch calls {
		case 1:
			return ""
		case 2:
			return "partial\n─────\n> \n─────\n  esc to cancel      Gemini · high\n"
		}
		return quotaLine + idleChrome
	}
	if got := TurnFailure(m, ""); got != quotaLine {
		t.Fatalf("got %q after %d captures", got, calls)
	}
	// An idle frame with no banner is a clean end and is not retried.
	calls = 0
	capturePaneFrame = func(string) string { calls++; return "done" + idleChrome }
	if got := TurnFailure(m, "7"); got != "" || calls != 1 {
		t.Fatalf("got %q after %d captures", got, calls)
	}
}
