package skillcurator

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// SchedConfig is when a pass may start.
type SchedConfig struct {
	// IdleAfter is how long no session may have run a turn before a pass starts.
	IdleAfter time.Duration
	// MinInterval separates two passes; ages are counted in days, so a pass is cheap and rare.
	MinInterval time.Duration
}

// DefaultSchedConfig is what production uses.
func DefaultSchedConfig() SchedConfig {
	return SchedConfig{IdleAfter: 15 * time.Minute, MinInterval: 6 * time.Hour}
}

// Deps are what main wires; an unwired curator never runs.
type Deps struct {
	// Enabled is the member's switch (ui-prefs agentSkillCuration, default off), read every tick.
	Enabled func() bool
	// Activity is the workspace's session-activity signal, the one the other idle schedules read.
	Activity func() (busy bool, last time.Time)
	// Roots lists the user skills roots of the agent kinds that have one.
	Roots func() []string
	// Now is the clock; nil means time.Now. The idle test inside a pass reads it afresh.
	Now func() time.Time
}

var (
	tickMu  sync.Mutex
	lastRun time.Time // in memory: a restart may run one early pass, which only archives what is due anyway
)

func idle(d Deps, c SchedConfig, now time.Time) bool {
	busy, last := d.Activity()
	return !busy && (last.IsZero() || now.Sub(last) >= c.IdleAfter)
}

// Tick is one period's decision and, when it says run, the pass. It reports whether it ran.
func Tick(d Deps, sc SchedConfig, cc Config, now time.Time) bool {
	if d.Enabled == nil || d.Activity == nil || d.Roots == nil {
		return false
	}
	tickMu.Lock()
	defer tickMu.Unlock()
	if !d.Enabled() || !idle(d, sc, now) || (!lastRun.IsZero() && now.Sub(lastRun) < sc.MinInterval) {
		return false
	}
	clock := d.Now
	if clock == nil {
		clock = time.Now
	}
	stopped := false
	seen := map[string]bool{}
	for _, root := range d.Roots() {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		// Re-checked per skill: a turn that began meanwhile ends the pass.
		res := Run(root, now, cc, func() bool { return d.Enabled() && idle(d, sc, clock()) })
		stopped = stopped || res.Stopped
		if len(res.Stale)+len(res.Archived)+len(res.Revived) > 0 {
			log.Printf("skill curator: %s: marked stale [%s], archived [%s], no longer stale [%s]", root,
				strings.Join(res.Stale, " "), strings.Join(res.Archived, " "), strings.Join(res.Revived, " "))
		}
	}
	// A pass cut short does not start the MinInterval wait: it should resume at the next idle tick.
	if !stopped {
		lastRun = now
	}
	return true
}

// Start launches the polling loop; the switch is read every tick, so turning it off stops the
// next pass at once.
func Start(ctx context.Context, d Deps) {
	go func() {
		select {
		case <-time.After(2 * time.Minute):
		case <-ctx.Done():
			return
		}
		for {
			Tick(d, DefaultSchedConfig(), DefaultConfig(), time.Now())
			select {
			case <-time.After(time.Minute):
			case <-ctx.Done():
				return
			}
		}
	}()
}
