package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiftedEarlyFrom(t *testing.T) {
	episode := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := episode.Add(time.Hour)
	booked := time.Date(2026, 11, 4, 21, 7, 0, 0, time.UTC) // the weekly reset the booking waits for
	const weekly = "banner+capture:weekly"
	// restarted: both windows low, the weekly one now resets a week from now, all observed after the episode.
	restarted := func() *capturedUsage {
		return &capturedUsage{
			FiveHour:           &capturedWindow{UsedPercent: 0, ResetsAt: now.Add(5 * time.Hour).Unix()},
			SevenDay:           &capturedWindow{UsedPercent: 0, ResetsAt: now.Add(7 * 24 * time.Hour).Unix()},
			CapturedAt:         now.Unix(),
			ObservedAt:         now.Unix(),
			FiveHourObservedAt: now.Unix(),
			SevenDayObservedAt: now.Unix(),
		}
	}
	mod := func(f func(*capturedUsage)) *capturedUsage { c := restarted(); f(c); return c }
	for _, c := range []struct {
		name   string
		c      *capturedUsage
		source string
		want   bool
	}{
		{"lifted: weekly window restarted", restarted(), weekly, true},
		{"still full", mod(func(c *capturedUsage) { c.SevenDay.UsedPercent = 100; c.SevenDay.ResetsAt = booked.Unix() }), weekly, false},
		{"other window still full", mod(func(c *capturedUsage) { c.FiveHour.UsedPercent = 97 }), weekly, false},
		{"reading older than the episode", mod(func(c *capturedUsage) {
			c.SevenDayObservedAt = episode.Add(-time.Minute).Unix()
		}), weekly, false},
		{"only the capture-wide time is old (legacy capture)", mod(func(c *capturedUsage) {
			c.SevenDayObservedAt, c.ObservedAt, c.CapturedAt = 0, episode.Add(-time.Hour).Unix(), episode.Add(-time.Hour).Unix()
		}), weekly, false},
		{"window unchanged", mod(func(c *capturedUsage) { c.SevenDay.ResetsAt = booked.Unix() }), weekly, false},
		{"window missing", mod(func(c *capturedUsage) { c.SevenDay = nil }), weekly, false},
		{"banner-only source cannot be judged", restarted(), "banner", false},
		{"unlabelled capture cannot be judged", restarted(), "capture", false},
		{"already moved booking", restarted(), "codex:reset-early", false},
		{"no reading", nil, weekly, false},
	} {
		if got := liftedEarlyFrom(c.c, c.source, booked, episode); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLiftedEarlyFiveHourSource(t *testing.T) {
	episode := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := episode.Add(time.Hour)
	booked := episode.Add(3 * time.Hour)
	c := &capturedUsage{
		FiveHour:           &capturedWindow{UsedPercent: 2, ResetsAt: now.Add(5 * time.Hour).Unix()},
		SevenDay:           &capturedWindow{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour).Unix()},
		FiveHourObservedAt: now.Unix(),
		SevenDayObservedAt: episode.Add(-5 * time.Hour).Unix(), // unchanged and low: stays old, must not matter
	}
	if !liftedEarlyFrom(c, "capture:5h", booked, episode) {
		t.Error("five-hour restart with an old but low weekly reading must count as lifted")
	}
	c.FiveHourObservedAt = episode.Add(-time.Minute).Unix()
	if liftedEarlyFrom(c, "capture:5h", booked, episode) {
		t.Error("a stale five-hour reading must not count")
	}
}

// LiftedEarly reads af-usage.json itself: a missing file answers false, a written capture decides.
func TestLiftedEarlyReadsTheCaptureFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	signIn(t, cfg, "acct-a")
	episode := time.Now().Add(-time.Hour)
	booked := time.Now().Add(48 * time.Hour)
	if LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Fatal("missing capture file must answer false")
	}
	now := time.Now().Unix()
	writeCapturedUsage(capturedUsage{
		FiveHour:   &capturedWindow{UsedPercent: 1, ResetsAt: now + 5*3600},
		SevenDay:   &capturedWindow{UsedPercent: 1, ResetsAt: now + 7*24*3600},
		CapturedAt: now, ObservedAt: now, FiveHourObservedAt: now, SevenDayObservedAt: now,
	})
	if !LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Error("a fresh restarted capture must answer true")
	}
	if err := os.WriteFile(usageCapturePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Error("an unreadable capture must answer false")
	}
}

// ResetAt's source must name the captured window the instant came from, or LiftedEarly has
// nothing to judge by.
func TestLabelCaptureSource(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	fh := time.Date(2026, 11, 1, 19, 50, 0, 0, time.UTC)
	sd := fh.AddDate(0, 0, 4)
	writeCapturedUsage(capturedUsage{
		FiveHour: &capturedWindow{ResetsAt: fh.Unix()}, SevenDay: &capturedWindow{ResetsAt: sd.Unix()},
	})
	for _, c := range []struct {
		source string
		at     time.Time
		want   string
	}{
		{"banner+capture", fh, "banner+capture:5h"},
		{"banner+capture", sd, "banner+capture:weekly"},
		{"capture", sd, "capture:weekly"},
		{"banner", fh, "banner"},
		{"capture", fh.Add(time.Hour), "capture"},
	} {
		if got := labelCaptureSource(c.source, c.at); got != c.want {
			t.Errorf("labelCaptureSource(%q) = %q, want %q", c.source, got, c.want)
		}
	}
}

// The traits the sweep resolves must carry the question, or none of the above is ever asked.
func TestUsageLimitTraitsWiresLiftedEarly(t *testing.T) {
	if agentImpl.UsageLimitTraits(agentImpl{}).LiftedEarly == nil {
		t.Fatal("claude UsageLimitTraits.LiftedEarly is nil")
	}
}

func signIn(t *testing.T, cfg, uuid string) {
	t.Helper()
	b := `{"oauthAccount":{"accountUuid":"` + uuid + `"}}`
	if err := os.WriteFile(filepath.Join(cfg, ".claude.json"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A capture taken under another account must not lift this episode: A hit the weekly limit,
// the member signs in as B, and B's low usage and later resets_at land in the shared capture.
func TestLiftedEarlyIgnoresACaptureAfterAnAccountSwitch(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	signIn(t, cfg, "acct-a")
	_ = accountID()
	episode := time.Now().Add(-time.Hour)
	booked := time.Now().Add(48 * time.Hour)
	now := time.Now().Unix()
	writeCapturedUsage(capturedUsage{
		FiveHour:   &capturedWindow{UsedPercent: 1, ResetsAt: now + 5*3600},
		SevenDay:   &capturedWindow{UsedPercent: 1, ResetsAt: now + 7*24*3600},
		CapturedAt: now, ObservedAt: now, FiveHourObservedAt: now, SevenDayObservedAt: now,
	})
	if !LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Fatal("same account: want lifted")
	}
	signIn(t, cfg, "acct-b-longer-id") // the file's size changes, so the identity is re-read
	if LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Error("capture after an account switch must not count as lifted")
	}
	signIn(t, cfg, "") // no identity at all
	if LiftedEarly("banner+capture:weekly", booked, episode) {
		t.Error("unknown account must answer false")
	}
}
