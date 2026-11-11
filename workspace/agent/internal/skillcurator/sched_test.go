package skillcurator

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTickGates(t *testing.T) {
	root := t.TempDir()
	enabled, busy := true, false
	last := now.Add(-time.Hour)
	d := Deps{
		Enabled:  func() bool { return enabled },
		Activity: func() (bool, time.Time) { return busy, last },
		Roots:    func() []string { return []string{root, root, ""} },
		Now:      func() time.Time { return now },
	}
	sc := DefaultSchedConfig()
	reset := func() {
		lastRun = time.Time{}
		skill(t, root, "afa-old", agentBody(), days(100))
	}
	gone := func() bool { return !present(filepath.Join(root, "afa-old")) }

	enabled = false
	reset()
	if Tick(d, sc, DefaultConfig(), now) || gone() {
		t.Fatal("ran while disabled")
	}
	enabled, busy = true, true
	if Tick(d, sc, DefaultConfig(), now) || gone() {
		t.Fatal("ran while busy")
	}
	busy, last = false, now.Add(-time.Minute)
	if Tick(d, sc, DefaultConfig(), now) || gone() {
		t.Fatal("ran right after a turn")
	}
	last = now.Add(-time.Hour)
	if !Tick(d, sc, DefaultConfig(), now) || !gone() {
		t.Fatal("did not run when idle")
	}
	skill(t, root, "afa-old", agentBody(), days(100))
	if Tick(d, sc, DefaultConfig(), now.Add(time.Hour)) || gone() {
		t.Fatal("ran again inside MinInterval")
	}
	if !Tick(d, sc, DefaultConfig(), now.Add(7*time.Hour)) || !gone() {
		t.Fatal("did not run after MinInterval")
	}
	if Tick(Deps{}, sc, DefaultConfig(), now) {
		t.Fatal("unwired deps ran")
	}
}

// A pass cut short by a turn that begins meanwhile reads the clock afresh and does not start the
// MinInterval wait.
func TestTickStoppedPassRetriesNextTick(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-a", agentBody(), days(100))
	skill(t, root, "afa-b", agentBody(), days(100))
	clock := now
	last := now.Add(-time.Hour)
	calls := 0
	d := Deps{
		Enabled: func() bool { return true },
		Activity: func() (bool, time.Time) {
			calls++
			if calls == 3 { // the turn starts between the first and the second skill
				last = clock
			}
			return false, last
		},
		Roots: func() []string { return []string{root} },
		Now:   func() time.Time { clock = clock.Add(time.Second); return clock },
	}
	lastRun = time.Time{}
	if !Tick(d, DefaultSchedConfig(), DefaultConfig(), now) {
		t.Fatal("did not run")
	}
	if len(Archived(root)) != 1 {
		t.Fatalf("archived %v, want the pass cut after one", Archived(root))
	}
	if !lastRun.IsZero() {
		t.Fatal("a cut pass started the MinInterval wait")
	}
}
