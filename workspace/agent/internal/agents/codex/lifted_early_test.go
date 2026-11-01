package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiftedEarlyFrom(t *testing.T) {
	now := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	episode := now.Add(-time.Hour)
	booked := time.Date(2026, 11, 4, 21, 7, 0, 0, time.UTC)
	win := func(pct float64, resets time.Time) *usageWindow {
		return &usageWindow{Pct: pct, ResetsAt: resets.Format(time.RFC3339)}
	}
	restarted := usage{OK: true, ObservedAt: now, FiveHour: win(0, now.Add(5*time.Hour)), SevenDay: win(0, now.Add(7*24*time.Hour))}
	mod := func(f func(*usage)) usage { u := restarted; f(&u); return u }
	for _, c := range []struct {
		name   string
		u      usage
		source string
		want   bool
	}{
		{"restarted weekly window", restarted, "codex:weekly", true},
		{"still full", mod(func(u *usage) { u.SevenDay = win(100, booked) }), "codex:weekly", false},
		{"other window still full", mod(func(u *usage) { u.FiveHour = win(97, now.Add(time.Hour)) }), "codex:weekly", false},
		{"observed before the episode", mod(func(u *usage) { u.ObservedAt = episode.Add(-time.Minute) }), "codex:weekly", false},
		{"limit_reached asserted", mod(func(u *usage) { u.LimitReached = true }), "codex:weekly", false},
		{"window did not restart", mod(func(u *usage) { u.SevenDay = win(0, booked) }), "codex:weekly", false},
		{"already moved booking", restarted, "codex:reset-early", false},
		{"no reading", usage{}, "codex:weekly", false},
	} {
		if got := liftedEarlyFrom(c.u, c.source, booked, episode); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRolloutCleanTurnSince(t *testing.T) {
	at := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	const later = "2026-10-30T03:00:00.000Z"
	for _, c := range []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"clean turn after", [][]byte{taskStarted(later, "t3"), taskComplete(later, "t3")}, true},
		{"clean turn before the episode", [][]byte{taskComplete("2026-10-30T01:00:00.000Z", "t1")}, false},
		{"the limit failure itself", [][]byte{taskFailed(later, "t2", limitErrSnake)}, false},
		{"a turn still running", [][]byte{taskComplete(later, "t3"), taskStarted(later, "t4")}, false},
		{"empty", nil, false},
	} {
		if got := rolloutCleanTurnSince(c.lines, at); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

// LiftedEarly must neither fetch nor wait on accountUsage's mutex (held across an HTTP call):
// the sweep calls it under a lifecycle lease and the episode lock. It reads the published
// snapshot, and refuses a source it cannot judge before reading anything.
func TestLiftedEarlyNeverBlocksOnTheAccountFetch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"x","account_id":"acct-early"}}`
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	booked := now.Add(48 * time.Hour)
	win := func(pct float64, resets time.Time) *usageWindow {
		return &usageWindow{Pct: pct, ResetsAt: resets.Format(time.RFC3339)}
	}
	old := accountPublished.Load()
	t.Cleanup(func() { accountPublished.Store(old) })
	accountPublished.Store(&accountPub{account: "acct-early", observed: now, val: usage{
		OK: true, FiveHour: win(0, now.Add(5*time.Hour)), SevenDay: win(0, now.Add(7*24*time.Hour)),
	}})

	accountUsageCache.Lock() // an in-flight fetch holds this
	defer accountUsageCache.Unlock()
	got := make(chan bool, 2)
	go func() { got <- LiftedEarly("codex:weekly", booked, now.Add(-time.Hour)) }()
	go func() { got <- LiftedEarly("codex:reset-early", booked, now.Add(-time.Hour)) }()
	var res []bool
	for i := 0; i < 2; i++ {
		select {
		case v := <-got:
			res = append(res, v)
		case <-time.After(2 * time.Second):
			t.Fatal("LiftedEarly blocked on the account cache lock")
		}
	}
	if !(res[0] != res[1]) {
		t.Errorf("results = %v, want exactly the weekly source to report lifted", res)
	}
}
