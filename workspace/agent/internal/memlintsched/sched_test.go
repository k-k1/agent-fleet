package memlintsched

import (
	"errors"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

type rig struct {
	enabled bool
	busy    bool
	last    time.Time
	runs    int
	out     memoryx.LintApplyAllOut
	err     error
}

func newRig(t *testing.T) (*rig, Deps) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	r := &rig{enabled: true, last: t0.Add(-time.Hour)}
	return r, Deps{
		Enabled:  func() bool { return r.enabled },
		Activity: func() (bool, time.Time) { return r.busy, r.last },
		Run: func(time.Time, func() bool) (memoryx.LintApplyAllOut, error) {
			r.runs++
			return r.out, r.err
		},
	}
}

func TestDecide(t *testing.T) {
	c := DefaultConfig()
	cases := []struct {
		name    string
		enabled bool
		busy    bool
		last    time.Time
		st      State
		want    string
	}{
		{"off", false, false, t0.Add(-time.Hour), State{}, "disabled"},
		{"busy", true, true, t0.Add(-time.Hour), State{}, "busy"},
		{"turn a minute ago", true, false, t0.Add(-time.Minute), State{}, "not-idle"},
		{"waiting", true, false, t0.Add(-time.Hour), State{NotBefore: t0.Add(time.Minute)}, "too-soon"},
		{"idle", true, false, t0.Add(-time.Hour), State{NotBefore: t0}, "idle"},
		{"never active", true, false, time.Time{}, State{}, "idle"},
	}
	for _, tc := range cases {
		if _, got := Decide(tc.enabled, t0, tc.last, tc.busy, tc.st, c); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// The switch is the only way in: off means no run and no state file, busy and recent activity
// defer the run, and a run happens once per MinInterval (the window survives a restart because it
// is read from the state file).
func TestTickRunsOnlyWhenIdleAndOnce(t *testing.T) {
	r, d := newRig(t)
	c := DefaultConfig()

	r.enabled = false
	if Tick(d, c, t0) || r.runs != 0 {
		t.Fatal("ran with the switch off")
	}
	r.enabled, r.busy = true, true
	if Tick(d, c, t0) || r.runs != 0 {
		t.Fatal("ran while busy")
	}
	r.busy, r.last = false, t0.Add(-time.Minute)
	if Tick(d, c, t0) || r.runs != 0 {
		t.Fatal("ran right after a turn")
	}
	r.last = t0.Add(-time.Hour)
	if !Tick(d, c, t0) || r.runs != 1 {
		t.Fatal("did not run when idle")
	}
	// Same state file, as after a restart: the window holds.
	if Tick(d, c, t0.Add(c.MinInterval-time.Minute)) || r.runs != 1 {
		t.Fatal("ran again inside MinInterval")
	}
	if !Tick(d, c, t0.Add(c.MinInterval)) || r.runs != 2 {
		t.Fatal("did not run after MinInterval")
	}
}

// A failing run is not retried on the next tick, and the wait doubles up to the cap.
func TestTickBacksOffAfterFailure(t *testing.T) {
	r, d := newRig(t)
	c := DefaultConfig()
	r.err = errors.New("boom")
	now := t0
	var waits []time.Duration
	for i := 0; i < 6; i++ {
		if !Tick(d, c, now) {
			t.Fatalf("attempt %d did not run", i)
		}
		if Tick(d, c, now.Add(time.Minute)) {
			t.Fatalf("attempt %d: retried on the next tick", i)
		}
		w := loadState().NotBefore.Sub(now)
		waits = append(waits, w)
		now = now.Add(w)
	}
	if waits[0] != c.RetryBase || waits[1] != 2*c.RetryBase || waits[5] != c.RetryMax {
		t.Errorf("waits = %v", waits)
	}
	// A success clears the failure count.
	r.err = nil
	if !Tick(d, c, now) || loadState().Failures != 0 {
		t.Errorf("state after success = %+v", loadState())
	}
}

// A run that hit the write cap comes back sooner than MinInterval; a manual run holding the lock
// spends nothing; a run cut short by activity is not a failure.
func TestTickOutcomes(t *testing.T) {
	r, d := newRig(t)
	c := DefaultConfig()
	r.out = memoryx.LintApplyAllOut{Deferred: 3}
	Tick(d, c, t0)
	if got := loadState().NotBefore.Sub(t0); got != c.ContinueAfter {
		t.Errorf("deferred: next in %s", got)
	}
	r.out = memoryx.LintApplyAllOut{Stopped: true}
	Tick(d, c, t0.Add(time.Hour))
	if st := loadState(); st.Failures != 0 || st.LastStatus != "stopped" {
		t.Errorf("stopped: %+v", st)
	}
	r.out, r.err = memoryx.LintApplyAllOut{}, memoryx.ErrLintRunning
	if Tick(d, c, t0.Add(2*time.Hour)) {
		t.Error("a refused run counts as having run")
	}
	if st := loadState(); st.Failures != 0 || !st.NotBefore.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("ErrLintRunning spent the window: %+v", st)
	}
}

// An unwired schedule never runs.
func TestTickUnwired(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if Tick(Deps{}, DefaultConfig(), t0) {
		t.Fatal("ran unwired")
	}
}
