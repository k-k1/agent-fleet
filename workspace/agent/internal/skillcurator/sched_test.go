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
