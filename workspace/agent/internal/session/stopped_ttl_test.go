package session

import (
	"testing"
	"time"
)

// Precedence: the user's setting, then AF_SESSION_STOPPED_TTL, then the 7-day default. A stored
// value no Console button produces is "not set", so it defers to the env var rather than being
// read as some nearby period.
func TestStoppedTTLPrecedence(t *testing.T) {
	old := StoppedArchiveDaysPref
	t.Cleanup(func() { StoppedArchiveDaysPref = old })

	for _, tc := range []struct {
		name   string
		pref   func() int // nil = unwired
		env    string
		want   time.Duration
		wantOn bool
	}{
		{"nothing set", nil, "", StoppedArchiveDefault, true},
		{"unset pref", func() int { return 0 }, "", StoppedArchiveDefault, true},
		{"env only", func() int { return 0 }, "36h", 36 * time.Hour, true},
		{"unwired pref, env", nil, "36h", 36 * time.Hour, true},
		{"setting beats env", func() int { return 14 }, "36h", 14 * 24 * time.Hour, true},
		{"setting beats default", func() int { return 1 }, "", 24 * time.Hour, true},
		{"off beats env", func() int { return StoppedArchiveNever }, "36h", 0, false},
		{"unknown choice defers to env", func() int { return 2 }, "36h", 36 * time.Hour, true},
		{"unknown choice defers to default", func() int { return 365 }, "", StoppedArchiveDefault, true},
		{"malformed env defers to default", func() int { return 0 }, "a week", StoppedArchiveDefault, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			StoppedArchiveDaysPref = tc.pref
			t.Setenv("AF_SESSION_STOPPED_TTL", tc.env)
			d, on := StoppedTTL()
			if on != tc.wantOn || (on && d != tc.want) {
				t.Fatalf("StoppedTTL() = %v, %v; want %v, %v", d, on, tc.want, tc.wantOn)
			}
		})
	}
}

// Read afresh every call: the list handler applies a changed setting on its next poll, with no
// Agent restart, only because nothing snapshots the answer.
func TestStoppedTTLIsReadOnEveryCall(t *testing.T) {
	old := StoppedArchiveDaysPref
	t.Cleanup(func() { StoppedArchiveDaysPref = old })
	t.Setenv("AF_SESSION_STOPPED_TTL", "")

	n := 3
	StoppedArchiveDaysPref = func() int { return n }
	if d, _ := StoppedTTL(); d != 3*24*time.Hour {
		t.Fatalf("period = %v, want 72h", d)
	}
	n = StoppedArchiveNever
	if _, on := StoppedTTL(); on {
		t.Fatal("auto-archive still on after the setting changed to off (snapshotted?)")
	}
}

func TestNormalizeStoppedArchiveDays(t *testing.T) {
	for _, n := range StoppedArchiveDayChoices {
		if got := NormalizeStoppedArchiveDays(n); got != n {
			t.Errorf("choice %d normalized to %d", n, got)
		}
	}
	for _, tc := range []struct{ in, want int }{
		{StoppedArchiveNever, StoppedArchiveNever},
		{0, 0},
		{-2, 0},
		{2, 0},
		{31, 0},
	} {
		if got := NormalizeStoppedArchiveDays(tc.in); got != tc.want {
			t.Errorf("NormalizeStoppedArchiveDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
