package memconsol

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
)

// Deps are the Agent's own facilities the trigger reuses; main wires them (memconsol_wiring.go) so
// this package does not import the session machinery. Nothing here has a default that would let a
// run start: an unwired Deps never runs.
type Deps struct {
	// Enabled is the member's switch (ui-prefs memoryConsolidation, with agent memory on). Read on
	// every tick, so switching it off stops the next run at once.
	Enabled func() bool
	// Activity is the existing session-activity signal: whether any session is working right now,
	// and the newest time any session's state changed (a turn started or ended).
	Activity func() (busy bool, last time.Time)
	// Models builds the two roles; tests return fakes. nil means the configured runner.
	Models func() (Models, map[string]string, error)
	// Notice is the hook for "at most once a day while any proposal waits"; the notification
	// itself is the next PR's. It is called with the number of pending proposals, and only when
	// the daily gate allows it.
	Notice func(waiting int)
}

var (
	depsMu sync.Mutex
	deps   Deps
)

// Configure installs the Deps (once, from main's init).
func Configure(d Deps) {
	depsMu.Lock()
	deps = d
	depsMu.Unlock()
}

func currentDeps() Deps {
	depsMu.Lock()
	defer depsMu.Unlock()
	return deps
}

// Decision is the trigger's answer for one tick.
type Decision struct {
	Run    bool
	Reason string
}

// Decide is the trigger rule of ADR 0130 decision 6 as a pure function: the switch is on, no
// session is working, none has run a turn for IdleAfter, and the last run began at least
// MinInterval ago. The idle conditions are not optional: there is no input that skips them, and
// the only caller of Run on the Agent is the tick that asked this.
func Decide(enabled bool, now, lastRun, lastActivity time.Time, busy bool, c Config) Decision {
	switch {
	case !enabled:
		return Decision{false, "disabled"}
	case busy:
		return Decision{false, "busy"}
	case !lastActivity.IsZero() && now.Sub(lastActivity) < c.IdleAfter:
		return Decision{false, "not-idle"}
	case !lastRun.IsZero() && now.Sub(lastRun) < c.MinInterval:
		return Decision{false, "too-soon"}
	}
	return Decision{true, "idle"}
}

const (
	tickEvery   = time.Minute
	startupWait = 90 * time.Second
)

func tickInterval() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("AF_MEMORY_CONSOLIDATION_TICK"))); err == nil && d >= time.Second {
		return d
	}
	return tickEvery
}

var runMu sync.Mutex // one run at a time

// Start launches the polling loop, the same shape as the memory snapshot loop (the Agent's
// existing facility for "when quiet"); it adds no second scheduler. The switch is read every tick.
func Start(ctx context.Context) {
	go func() {
		select {
		case <-time.After(startupWait):
		case <-ctx.Done():
			return
		}
		every := tickInterval()
		for {
			Tick(ctx, time.Now())
			select {
			case <-time.After(every):
			case <-ctx.Done():
				return
			}
		}
	}()
}

// turns is the production Turns: a turn started since t, from the same activity signal the idle
// test uses.
type turns struct{ act func() (bool, time.Time) }

func (t turns) StartedSince(since time.Time) bool {
	busy, last := t.act()
	return busy || last.After(since)
}

// Tick is one period's decision and, when it says run, the run. It returns the report of a run it
// made.
func Tick(ctx context.Context, now time.Time) (Report, bool) {
	d := currentDeps()
	if d.Enabled == nil || d.Activity == nil {
		return Report{}, false
	}
	cfg := LoadConfig()
	busy, last := d.Activity()

	stateMu.Lock()
	st, err := loadState()
	if err != nil {
		stateMu.Unlock()
		log.Printf("memory consolidation: state unreadable: %v", err)
		return Report{}, false
	}
	if !st.Trigger.LastTick.IsZero() && now.Sub(st.Trigger.LastTick) > 10*tickInterval() {
		st.Trigger.StoppedGap = true
	}
	st.Trigger.LastTick = now
	dec := Decide(d.Enabled(), now, st.Trigger.LastRun, last, busy, cfg)
	switch dec.Reason {
	case "busy", "not-idle":
		st.Trigger.BusyTicks++
	}
	if dec.Run {
		st.Trigger.LastRun = now
	}
	_ = st.save()
	stateMu.Unlock()
	if !dec.Run {
		return Report{}, false
	}

	runMu.Lock()
	defer runMu.Unlock()
	rep := doRun(ctx, d, cfg, now)

	stateMu.Lock()
	if st, err := loadState(); err == nil {
		st.Trigger.BusyTicks, st.Trigger.StoppedGap = 0, false
		_ = st.save()
	}
	stateMu.Unlock()
	maybeNotice(d, cfg, now)
	return rep, true
}

func doRun(ctx context.Context, d Deps, cfg Config, now time.Time) Report {
	var (
		m     Models
		roles map[string]string
		err   error
	)
	if d.Models != nil {
		m, roles, err = d.Models()
	} else {
		m, roles, err = configuredModels(cfg)
	}
	if err != nil {
		// A retired or unmapped model fails the run before any call: nothing is queued.
		rep := Report{ID: newID(), Started: now, Finished: now, Status: RunFailed, Error: "the model roles could not be set up",
			Handled: []string{}, Queued: []string{}, Delayed: []Delay{}}
		_ = writeReport(rep)
		log.Printf("memory consolidation: %v", err)
		return rep
	}
	rep, err := Run(ctx, RunInput{Models: m, Roles: roles, Turns: turns{d.Activity}, Enabled: d.Enabled, Config: cfg, Now: now})
	if err != nil {
		log.Printf("memory consolidation: run: %v", err)
	}
	return rep
}

// configuredModels builds the two roles from models.json (or the embedded default): the claude
// CLI headless with CLAUDE_CODE_DISABLE_CLAUDE_MDS=1, as goldeval runs it.
func configuredModels(cfg Config) (Models, map[string]string, error) {
	ec, err := LoadEngineConfig()
	if err != nil {
		return Models{}, nil, err
	}
	var out Models
	roles := map[string]string{}
	for _, r := range memengine.Roles() {
		cmd, err := ec.Command(ec.Roles[r])
		if err != nil {
			return Models{}, nil, err
		}
		cmd.Timeout = cfg.CallTimeout
		roles[string(r)] = ec.Roles[r]
		if r == memengine.RoleSmall {
			out.Small = cmd
		} else {
			out.Large = cmd
		}
	}
	return out, roles, nil
}

// maybeNotice is the hook for the daily "proposals are waiting" notice: at most once per
// Config.NoticeEvery while any proposal is pending. No notification is raised in this PR; the
// next one installs Deps.Notice.
func maybeNotice(d Deps, cfg Config, now time.Time) {
	if d.Notice == nil {
		return
	}
	ps, err := ListProposals(StatusPending, now)
	if err != nil || len(ps) == 0 {
		return
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := loadState()
	if err != nil || (!st.Trigger.LastNotice.IsZero() && now.Sub(st.Trigger.LastNotice) < cfg.NoticeEvery) {
		return
	}
	st.Trigger.LastNotice = now
	if st.save() == nil {
		d.Notice(len(ps))
	}
}
