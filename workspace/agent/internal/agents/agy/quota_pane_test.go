package agy

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
		{"bulleted quote of the full text",
			"- " + quotaLine + idleChrome, false},
		{"markdown quote of the full text above the composer",
			"> " + quotaLine + idleChrome, false},
		{"line before ends with individual",
			"A message about an individual\nquota reached. Please upgrade your subscription to increase your limits." + idleChrome, false},
		{"phrase then an ordinary reply (no panic)",
			"Individual quota reached\nHere is the explanation." + idleChrome, false},
		{"wrap inside the phrase and inside a number",
			"Individual quota\nreached. Please upgrade your subscription to increase your\nlimits. Resets in 1h3\n9m46s." + idleChrome, true},
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
	clock := t0
	oldNow := quotaNow
	defer func() { quotaNow = oldNow }()
	quotaNow = func() time.Time { return clock }
	at, src, ok := quotaResetAt(m, false, t0.Add(-time.Hour)) // the sweep's own start time is not the base
	want := t0.Add(time.Hour + 39*time.Minute + 46*time.Second)
	if !ok || !at.Equal(want) || src != quotaRefusalSource {
		t.Fatalf("got %v %q %v, want %v", at, src, ok, want)
	}
	clock = t0.Add(10 * time.Minute)
	if at2, _, _ := quotaResetAt(m, false, time.Time{}); !at2.Equal(want) {
		t.Fatalf("second poll moved the reset to %v", at2)
	}
	// The banner leaves the tail, then a new refusal with the same text counts from its own sighting.
	capturePaneFrame = func(string) string { return "ok" + idleChrome }
	if _, atLimit := atQuotaLimit(m); atLimit {
		t.Fatal("still at limit without a banner")
	}
	capturePaneFrame = func(string) string { return "x\n" + quotaLine + idleChrome }
	t1 := t0.Add(3 * time.Hour)
	clock = t1
	if at3, _, _ := quotaResetAt(m, false, time.Time{}); !at3.Equal(t1.Add(time.Hour + 39*time.Minute + 46*time.Second)) {
		t.Fatalf("new refusal reset %v", at3)
	}
}

func idleFrame() string { return "done" + idleChrome }

// An unreadable or unpainted pane right after the DB's last write holds the completion back; an
// idle pane, or a pane that stays unreadable past the grace, does not.
func TestTurnFailurePendingWaitsForThePaneOnlyBriefly(t *testing.T) {
	old, oldAlive := capturePaneFrame, paneSessionAlive
	defer func() { capturePaneFrame, paneSessionAlive = old, oldAlive }()
	paneSessionAlive = func(string) bool { return true }
	user, model := 14, 15
	rows := [][3]any{{user, stepStatusDone, []byte("x")}, {model, stepStatusDone, []byte("x")}}
	for _, tc := range []struct {
		name, frame string
		age         time.Duration
		want        string
	}{
		{"capture failed, DB just written", "", time.Second, agents.TurnFailurePending},
		{"footer not painted, DB just written", "partial\nesc to cancel\n", time.Second, agents.TurnFailurePending},
		{"idle footer", idleFrame(), time.Second, ""},
		{"capture failed, past the grace", "", 5 * time.Minute, ""},
		{"no tmux session at all", "\x00", time.Second, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := staleFixture(t, "slot-pend", rows, [][]byte{executorRow(4, 1)}, tc.age)
			capturePaneFrame = func(string) string { return strings.TrimPrefix(tc.frame, "\x00") }
			paneSessionAlive = func(string) bool { return !strings.HasPrefix(tc.frame, "\x00") }
			if got := quotaTurnFailure(m); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// A booking is cancelled only by a newer turn that COMPLETED: an input that landed, a turn still
// running, an interrupted run and a bare DB write keep it.
func TestRanTurnSinceNeedsACompletedRun(t *testing.T) {
	old := capturePaneFrame
	defer func() { capturePaneFrame = old }()
	user, model := 14, 15
	rows := [][3]any{{user, stepStatusDone, []byte("x")}, {model, stepStatusDone, []byte("x")}}
	for _, tc := range []struct {
		name  string
		turns [][]byte
		frame string
		want  bool
	}{
		{"completed run, idle", [][]byte{executorRow(4, 1)}, idleFrame(), true},
		{"completed run but the screen is generating", [][]byte{executorRow(4, 1)}, "x\nesc to cancel\n", false},
		{"run interrupted by Esc", [][]byte{executorRow(2, 1)}, idleFrame(), false},
		{"newest run ended before the last step", [][]byte{executorRow(4, 0)}, idleFrame(), false},
		{"no run recorded", nil, idleFrame(), false},
		{"pane gone", [][]byte{executorRow(4, 1)}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, now := staleFixture(t, "slot-ran", rows, tc.turns, time.Minute)
			capturePaneFrame = func(string) string { return tc.frame }
			if got := ranTurnSince(m, now.Add(-time.Hour)); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// The write predates the episode: nothing newer ran.
	m, now := staleFixture(t, "slot-ran-old", rows, [][]byte{executorRow(4, 1)}, 2*time.Hour)
	capturePaneFrame = func(string) string { return idleFrame() }
	if ranTurnSince(m, now.Add(-time.Hour)) {
		t.Fatal("a write older than the episode counted as a newer turn")
	}
}

// Any wrap the TUI makes reads whole, and the reset comes out of the same text that was detected.
func TestQuotaBannerWrapsAreReadWhole(t *testing.T) {
	for _, tc := range []struct{ name, banner string }{
		{"width 40, break inside a word", "Individual quota reached. Please upgrade\nyour subscription to increase your limi\nts. Resets in 1h39m46s."},
		{"break inside the phrase and a word", "Individual quota\nreached. Please upgrade your subscript\nion to increase your limits. Resets in 1h39m46s."},
		{"break inside the number", "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h3\n9m46s."},
	} {
		text, ok := quotaBannerInFrame(tc.banner + idleChrome)
		if !ok {
			t.Errorf("%s: not detected", tc.name)
			continue
		}
		if d, ok := quotaResetIn(text); !ok || d != time.Hour+39*time.Minute+46*time.Second {
			t.Errorf("%s: reset %v ok=%v", tc.name, d, ok)
		}
	}
}

// The same refusal keeps its first sighting through a re-wrap and through one failed capture; only
// an idle frame without the banner forgets it.
func TestQuotaFirstSightingSurvivesRewrapAndAFailedCapture(t *testing.T) {
	old, oldNow := capturePaneFrame, quotaNow
	defer func() { capturePaneFrame, quotaNow = old, oldNow }()
	m := session.Meta{Name: "qr3", Dir: t.TempDir()}
	defer forgetQuota(m.Name)
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	clock := t0
	quotaNow = func() time.Time { return clock }
	capturePaneFrame = func(string) string { return quotaLine + idleChrome }
	want, _, ok := quotaResetAt(m, false, time.Time{})
	if !ok {
		t.Fatal("no reset")
	}
	clock = t0.Add(5 * time.Minute)
	// resized: the same banner wrapped inside "limits"
	capturePaneFrame = func(string) string {
		return "Individual quota reached. Please upgrade your subscription to increase your limi\nts. Resets in 1h39m46s." + idleChrome
	}
	if got, _, _ := quotaResetAt(m, false, time.Time{}); !got.Equal(want) {
		t.Fatalf("re-wrap moved the reset: %v vs %v", got, want)
	}
	// one failed capture and one unpainted frame
	for _, f := range []string{"", "x\nesc to cancel\n"} {
		capturePaneFrame = func(string) string { return f }
		if _, at := atQuotaLimit(m); at {
			t.Fatal("limit read from a bad frame")
		}
	}
	clock = t0.Add(6 * time.Minute)
	capturePaneFrame = func(string) string { return quotaLine + idleChrome }
	if got, _, _ := quotaResetAt(m, false, time.Time{}); !got.Equal(want) {
		t.Fatalf("a failed capture moved the reset: %v vs %v", got, want)
	}
}

// A run that completed 500 ms after the episode opened counts (no 2 s floor).
func TestRanTurnSinceCountsAShortTurnRightAfterTheEpisode(t *testing.T) {
	old := capturePaneFrame
	defer func() { capturePaneFrame = old }()
	rows := [][3]any{{14, stepStatusDone, []byte("x")}, {15, stepStatusDone, []byte("x")}}
	m, now := staleFixture(t, "slot-short", rows, [][]byte{executorRow(4, 1)}, time.Minute)
	capturePaneFrame = func(string) string { return idleFrame() }
	at := now.Add(-time.Minute).Add(-500 * time.Millisecond)
	if !ranTurnSince(m, at) {
		t.Fatal("a turn completed 500ms after the episode was ignored")
	}
}
