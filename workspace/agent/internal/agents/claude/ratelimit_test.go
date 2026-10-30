package claude

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata (%s): %v", name, err)
	}
	return loc
}

// TestParseResetClock reads the reset wall-clock time out of the banner. Built around the
// shape actually observed (session limit), pinning the 12-hour boundaries and the wording
// variants.
func TestParseResetClock(t *testing.T) {
	mustLoc(t, "Asia/Tokyo") // without tzdata this table measures nothing
	for _, tc := range []struct {
		name         string
		msg          string
		wantH, wantM int
		wantZone     string
		wantUnparsed bool
	}{
		{
			name:  "observed (2026-07-31 / s5jjqv4)",
			msg:   "You've hit your session limit · resets 7:50pm (Asia/Tokyo)",
			wantH: 19, wantM: 50, wantZone: "Asia/Tokyo",
		},
		{
			name:  "no minutes",
			msg:   "You've hit your session limit · resets 9am (Asia/Tokyo)",
			wantH: 9, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// Reworded 5-hour window (measured sutgu3b 2026-10-30, issue #3065):
			// the clock still parses the same way.
			name:  "5-hour limit banner",
			msg:   "You've hit your 5-hour limit · resets 1:50am (Asia/Tokyo)",
			wantH: 1, wantM: 50, wantZone: "Asia/Tokyo",
		},
		{
			// 12am/12pm is the %12 boundary. Get it wrong and the wake-up is 12 hours off.
			name:  "12am is hour 0",
			msg:   "resets 12am (Asia/Tokyo)",
			wantH: 0, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			name:  "12pm is hour 12",
			msg:   "resets 12pm (Asia/Tokyo)",
			wantH: 12, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// The dated shape a weekly limit could take (not observed yet, defensive). The date
			// is dropped, the time is kept.
			name:  "with a date",
			msg:   "You've reached your weekly limit · resets Aug 3 at 9am (Asia/Tokyo)",
			wantH: 9, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// No zone in the text means the container's local zone (the one claude renders in).
			name:  "no timezone",
			msg:   "resets 7:50pm",
			wantH: 19, wantM: 50, wantZone: time.Local.String(),
		},
		{
			// am/pm is required, so unrelated numbers are never picked up.
			name: "no am/pm is not read", msg: "resets in 4 hours", wantUnparsed: true,
		},
		{
			name: "an abort error that is not a limit", msg: "API Error: Connection closed mid-response.", wantUnparsed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, loc, ok := parseResetClock(tc.msg)
			if ok == tc.wantUnparsed {
				t.Fatalf("parseResetClock ok = %v, want %v", ok, !tc.wantUnparsed)
			}
			if tc.wantUnparsed {
				return
			}
			if h != tc.wantH || m != tc.wantM {
				t.Errorf("clock = %02d:%02d, want %02d:%02d", h, m, tc.wantH, tc.wantM)
			}
			if loc.String() != tc.wantZone {
				t.Errorf("zone = %s, want %s", loc, tc.wantZone)
			}
		})
	}
}

// TestResolveResetAt covers how "when does it lift" is decided.
func TestResolveResetAt(t *testing.T) {
	jst := mustLoc(t, "Asia/Tokyo")
	banner := "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	// The observed abort time (the moment the banner was written).
	abortedAt := time.Date(2026, 7, 30, 18, 8, 48, 0, jst)
	want := time.Date(2026, 7, 30, 19, 50, 0, 0, jst)

	t.Run("banner alone", func(t *testing.T) {
		got, src, ok := resolveResetAt(banner, abortedAt, nil, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(want) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, want)
		}
		if src != "banner" {
			t.Errorf("source = %q, want banner", src)
		}
	})

	t.Run("a captured epoch pointing at the same wall clock wins", func(t *testing.T) {
		// The 5-hour window is 19:50, the weekly one is days away. Mixing up the two happens here.
		captured := []time.Time{want.Add(37 * time.Second), want.AddDate(0, 0, 4)}
		got, src, ok := resolveResetAt(banner, abortedAt, captured, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(captured[0]) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, captured[0])
		}
		if src != "banner+capture" {
			t.Errorf("source = %q, want banner+capture", src)
		}
	})

	t.Run("an abandoned menu does not push a past reset to the next day", func(t *testing.T) {
		// How this broke in production: the menu is found after sitting there for ~16 hours.
		// Anchored on now, 19:50 turns into "tomorrow 19:50" and the wait becomes a full day.
		now := abortedAt.Add(16 * time.Hour)
		got, _, ok := resolveResetAt(banner, abortedAt, nil, now)
		if !ok {
			t.Fatal("ok = false")
		}
		if !got.Equal(want) {
			t.Fatalf("resolveResetAt = %v, want %v (the 19:50 right after the abort)", got, want)
		}
		if !got.Before(now) {
			t.Error("a reset already in the past came back as a future time - the caller never falls into an immediate resume")
		}
	})

	t.Run("unreadable banner falls back to a captured future window", func(t *testing.T) {
		now := abortedAt
		captured := []time.Time{abortedAt.Add(-time.Hour), abortedAt.Add(3 * time.Hour)}
		got, src, ok := resolveResetAt("You've hit some new limit wording", abortedAt, captured, now)
		if !ok || !got.Equal(captured[1]) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, captured[1])
		}
		if src != "capture" {
			t.Errorf("source = %q, want capture", src)
		}
	})

	// The weekly window (observed corpus "You've hit your weekly limit · resets 9am (Asia/Tokyo)").
	// The banner's wall clock can only be read as "9am today or tomorrow", but a weekly reset can
	// be days away. Waking at 9am tomorrow hits the same 429, and every time a new episode redraws
	// the reservation - one burnt turn a day until the real reset (docs/log/47 §4-10).
	t.Run("weekly is not decided from the banner alone", func(t *testing.T) {
		weekly := "You've hit your weekly limit · resets 9am (Asia/Tokyo)"
		if at, src, ok := resolveResetAt(weekly, abortedAt, nil, abortedAt.Add(time.Minute)); ok {
			t.Errorf("resolveResetAt = %v (%s, ok=true) - betting on 9am tomorrow", at, src)
		}
		// A captured epoch pointing at the same wall clock fixes the date, so then it does answer.
		real := time.Date(2026, 8, 3, 9, 0, 0, 0, jst)
		got, src, ok := resolveResetAt(weekly, abortedAt, []time.Time{real}, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(real) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, real)
		}
		if src != "banner+capture" {
			t.Errorf("source = %q, want banner+capture", src)
		}
	})

	t.Run("nothing to go on, no answer", func(t *testing.T) {
		if _, _, ok := resolveResetAt("", time.Time{}, nil, abortedAt); ok {
			t.Error("ok = true - waking at a guessed time only hits the limit again")
		}
		// Nor when every captured epoch is in the past (stale, never refreshed).
		if _, _, ok := resolveResetAt("unknown", abortedAt, []time.Time{abortedAt.Add(-time.Hour)}, abortedAt); ok {
			t.Error("ok = true - a past epoch was adopted as the resume time")
		}
	})
}

// TestLimitInMessage pins the headless reading to the session markers: a window with its
// reset, a spend cap without one, and the 429 that says it is not a usage limit.
func TestLimitInMessage(t *testing.T) {
	tokyo := mustLoc(t, "Asia/Tokyo")
	now := time.Date(2026, 10, 26, 18, 0, 0, 0, tokyo)
	cases := []struct {
		name    string
		msg     string
		kind    LimitKind
		resetAt time.Time
		ok      bool
	}{
		{"session window", "You've hit your session limit · resets 7:50pm (Asia/Tokyo)", LimitWindow,
			time.Date(2026, 10, 26, 19, 50, 0, 0, tokyo), true},
		{"per-model window, no time", "You've reached your Fable 5 limit. Run /usage-credits to continue", LimitWindow, time.Time{}, true},
		{"spend cap", "You've hit your org's monthly spend limit · run /usage-credits to raise it", LimitSpend, time.Time{}, true},
		{"temporary 429", "API Error: 429 temporarily limiting requests (not your usage limit)", "", time.Time{}, false},
		{"overloaded", "API Error: 529 Overloaded", "", time.Time{}, false},
	}
	for _, c := range cases {
		kind, at, ok := limitInMessage(c.msg, now, nil)
		if kind != c.kind || !at.Equal(c.resetAt) || ok != c.ok {
			t.Errorf("%s: got (%q, %v, %v), want (%q, %v, %v)", c.name, kind, at, ok, c.kind, c.resetAt, c.ok)
		}
	}
}

// TestLimitInMessageWithCapture runs the headless reading with statusline captures present,
// the case the router actually meets: a text with no time must not borrow one, and a banner
// read just after its minute must not roll over to tomorrow.
func TestLimitInMessageWithCapture(t *testing.T) {
	tokyo := mustLoc(t, "Asia/Tokyo")
	fiveHour := time.Date(2026, 10, 26, 21, 0, 0, 0, tokyo)
	banner := "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"

	// A per-model limit names no time; the 5-hour window's reset is not its reset.
	_, at, ok := limitInMessage("You've reached your Fable 5 limit. Run /usage-credits to continue",
		time.Date(2026, 10, 26, 18, 0, 0, 0, tokyo), []time.Time{fiveHour})
	if !ok || !at.IsZero() {
		t.Errorf("time-less limit: got (%v, %v), want no reset", at, ok)
	}

	// Read one second past the minute with no capture: lifting now, not tomorrow.
	_, at, _ = limitInMessage(banner, time.Date(2026, 10, 26, 19, 50, 1, 0, tokyo), nil)
	if !at.IsZero() {
		t.Errorf("banner just passed: reset = %v, want none (not tomorrow)", at)
	}

	// Read within the minute with the real instant captured: that instant.
	instant := time.Date(2026, 10, 26, 19, 50, 37, 0, tokyo)
	_, at, _ = limitInMessage(banner, time.Date(2026, 10, 26, 19, 50, 10, 0, tokyo), []time.Time{instant})
	if !at.Equal(instant) {
		t.Errorf("banner minute with capture: reset = %v, want %v", at, instant)
	}

	// Well before the clock: today's reset, as before.
	_, at, _ = limitInMessage(banner, time.Date(2026, 10, 26, 18, 0, 0, 0, tokyo), []time.Time{fiveHour})
	if want := time.Date(2026, 10, 26, 19, 50, 0, 0, tokyo); !at.Equal(want) {
		t.Errorf("banner ahead: reset = %v, want %v", at, want)
	}
}

func TestIsPerModelLimit(t *testing.T) {
	for msg, want := range map[string]bool{
		"You've reached your Fable 5 limit. Run /usage-credits to continue or switch models": true,
		"You've hit your session limit · resets 7:50pm (Asia/Tokyo)":                         false,
		"You've hit your weekly limit · resets 9am (Asia/Tokyo)":                             false,
		"You've hit your org's monthly spend limit · run /usage-credits to raise it":         false,
	} {
		if got := IsPerModelLimit(msg); got != want {
			t.Errorf("IsPerModelLimit(%q) = %v, want %v", msg, got, want)
		}
	}
}

// TestBannerGated: for a limit without a menu the reset instant is trusted only when it came
// from the banner. The statusline-capture fallback answers for the account's 5-hour/weekly
// windows, and a per-model limit is a different window; the form that comes with a menu (an
// account window) keeps using the capture.
func TestBannerGated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		ok     bool
		menu   bool
		want   bool
	}{
		{"no menu, capture only", "capture", true, false, false},
		{"no menu, banner", "banner", true, false, true},
		{"no menu, banner and capture", "banner+capture", true, false, true},
		{"menu, capture only", "capture", true, true, true},
		{"menu, nothing found", "", false, true, false},
		{"no menu, nothing found", "", false, false, false},
	} {
		if got := bannerGated(tc.source, tc.ok, tc.menu); got != tc.want {
			t.Errorf("%s: bannerGated = %v, want %v", tc.name, got, tc.want)
		}
	}
}
