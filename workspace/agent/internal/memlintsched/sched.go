// Package memlintsched runs `lint --apply` on its own timer (ADR 0130 decision 5: lint-written
// reasons appear without a member running a command; decision 6: when the workspace is idle).
//
// It is deliberately not a hook in the consolidator's tick (internal/memconsol). That tick is
// gated by the model-calling memoryConsolidation switch, a 3 h window and a state file that all
// belong to the proposal flow; lint calls no model, so it has its own switch (ui-prefs
// memoryLintSchedule, default off), its own window and its own state file.
package memlintsched

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Config holds the figures; they are starting values, like the consolidator's.
type Config struct {
	// IdleAfter is how long no session may have run a turn before a run starts.
	IdleAfter time.Duration
	// MinInterval separates the starts of two runs that finished everything they found.
	MinInterval time.Duration
	// ContinueAfter is the wait after a run that hit the write cap and left memories Deferred.
	ContinueAfter time.Duration
	// RetryBase and RetryMax bound the wait after a failed attempt: RetryBase doubled per
	// consecutive failure, never past RetryMax. Without it a persistent failure would retry on
	// every idle tick.
	RetryBase, RetryMax time.Duration
}

// DefaultConfig is what production uses.
func DefaultConfig() Config {
	return Config{IdleAfter: 15 * time.Minute, MinInterval: 6 * time.Hour, ContinueAfter: 30 * time.Minute,
		RetryBase: 15 * time.Minute, RetryMax: 6 * time.Hour}
}

// Deps are what main wires; an unwired schedule never runs.
type Deps struct {
	// Enabled is the member's switch with agent memory on, read on every tick.
	Enabled func() bool
	// Activity is the session-activity signal: whether any session is working now and when any
	// session's state last changed.
	Activity func() (busy bool, last time.Time)
	// Run is one lint --apply run over every scope; cont false stops it between scopes.
	Run func(now time.Time, cont func() bool) (memoryx.LintApplyAllOut, error)
}

// State is the schedule's file. It outlives a restart so a restart neither re-runs at once nor
// forgets a backoff.
type State struct {
	LastRun    time.Time `json:"lastRun,omitempty"`
	NotBefore  time.Time `json:"notBefore,omitempty"`
	Failures   int       `json:"failures,omitempty"`
	LastStatus string    `json:"lastStatus,omitempty"` // ok | stopped | failed
	Marked     int       `json:"marked,omitempty"`
	Renamed    int       `json:"renamed,omitempty"`
	Deferred   int       `json:"deferred,omitempty"`
}

func statePath() string {
	return filepath.Join(paths.ClaudeConfigDir(), "af-memory.work", "lint-schedule", "state.json")
}

func loadState() State {
	var st State
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &st) // unreadable ⇒ zero state: the worst case is one early run
	}
	return st
}

func (s State) save() error {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Decide is the trigger rule as a pure function: the switch is on, no session is working, none
// has run a turn for IdleAfter, and the wait the last attempt asked for has passed.
func Decide(enabled bool, now, lastActivity time.Time, busy bool, st State, c Config) (run bool, reason string) {
	switch {
	case !enabled:
		return false, "disabled"
	case busy:
		return false, "busy"
	case !lastActivity.IsZero() && now.Sub(lastActivity) < c.IdleAfter:
		return false, "not-idle"
	case now.Before(st.NotBefore):
		return false, "too-soon"
	}
	return true, "idle"
}

var tickMu sync.Mutex // ticks never overlap in-process; the cross-path lock is memoryx's

// Tick is one period's decision and, when it says run, the run. It reports whether it ran.
func Tick(d Deps, c Config, now time.Time) bool {
	if d.Enabled == nil || d.Activity == nil || d.Run == nil {
		return false
	}
	tickMu.Lock()
	defer tickMu.Unlock()
	if !d.Enabled() {
		return false
	}
	busy, last := d.Activity()
	st := loadState()
	if run, _ := Decide(true, now, last, busy, st, c); !run {
		return false
	}
	// The attempt is recorded before it starts: a crash mid-run then waits RetryBase on restart
	// instead of starting again at once.
	before := st
	st.LastRun, st.NotBefore = now, now.Add(c.RetryBase)
	if err := st.save(); err != nil {
		log.Printf("memory lint schedule: state not writable, skipping the run: %v", err)
		return false
	}
	cont := func() bool {
		if !d.Enabled() {
			return false
		}
		// The same rule as Decide: a turn that began and ended since the run started is
		// activity too, which a busy flag alone would miss.
		b, last := d.Activity()
		return !b && (last.IsZero() || now.Sub(last) >= c.IdleAfter)
	}
	out, err := d.Run(now, cont)
	switch {
	case errors.Is(err, memoryx.ErrLintRunning):
		// A manual run holds the lock; it is doing the work. Nothing is spent: the state
		// goes back to what it was before this attempt.
		_ = before.save()
		return false
	case err != nil:
		st.Failures++
		wait := c.RetryBase
		for i := 1; i < st.Failures && wait < c.RetryMax; i++ {
			wait *= 2
		}
		if wait > c.RetryMax {
			wait = c.RetryMax
		}
		st.NotBefore, st.LastStatus = now.Add(wait), "failed"
		// The error text can hold a path or a memory name; log the fact, not the content.
		log.Printf("memory lint schedule: run failed (attempt %d, next in %s)", st.Failures, wait)
	case out.Stopped:
		st.Failures, st.LastStatus, st.NotBefore = 0, "stopped", now
	default:
		st.Failures, st.LastStatus = 0, "ok"
		st.NotBefore = now.Add(c.MinInterval)
		if out.Deferred > 0 {
			st.NotBefore = now.Add(c.ContinueAfter)
		}
	}
	st.Marked, st.Renamed, st.Deferred = out.Marked, out.Renamed, out.Deferred
	_ = st.save()
	return true
}

const (
	tickEvery   = time.Minute
	startupWait = 2 * time.Minute
)

// Start launches the polling loop, the same shape as the consolidator's and the snapshot loop; it
// adds no second scheduler in the sense of a new facility. The switch is read every tick, so
// turning it off stops the next run at once and a restart needs nothing (the switch is a pref).
func Start(ctx context.Context, d Deps) {
	go func() {
		select {
		case <-time.After(startupWait):
		case <-ctx.Done():
			return
		}
		for {
			Tick(d, DefaultConfig(), time.Now())
			select {
			case <-time.After(tickEvery):
			case <-ctx.Done():
				return
			}
		}
	}()
}
