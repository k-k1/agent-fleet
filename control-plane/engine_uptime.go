package main

// engine_uptime.go — what the admin panel needs in order to say something true about a GPU
// that is asleep most of the time (ADR 0071).
//
// Two separate things live here, and they are separate because they answer to different
// clocks:
//
//   - the SAMPLER, which turns the on-demand controller's own tick into an hourly row.
//     Engine uptime was recorded nowhere at all before this: the controller read the service
//     every 30 seconds to decide whether to start or stop it and then threw the observation
//     away. It is the only thing in the CP that looks often enough to be a sampler, and it
//     costs one INSERT — the AWS call it would otherwise need has already been made.
//   - the PREDICTION, i.e. "when will this stop by itself". That is a pure function of the
//     stored demand mark and the tuning, and it must be computed from the SAME clamped idle
//     window the controller applies, or the panel counts down to a moment nothing happens at.
//
// The honesty rule that shapes both: never turn "not watched" into "not running".
// engine_hourly has no row for an hour the CP was down, and this file must not invent one;
// engineStopETA returns nothing rather than a guess whenever the answer is not knowable.

import (
	"context"
	"log"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineUptimeStore is the narrow port the controller writes through, so a test can record
// what was sampled without a database.
type engineUptimeStore interface {
	AddEngineHour(ctx context.Context, engineKey, hour string, d store.EngineHourCounters) error
}

// engineUptimeSample turns one observed service state into a tick's contribution.
//
// `secs` is what the tick can vouch for, and it is stored as the denominator rather than
// reconstructed later from samples x interval: the controller's interval is not constant
// (30 seconds idle, engineControlBusyInterval while starting or warming), so the reconstruction
// is wrong in exactly the hours somebody opens the panel to look at.
//
// The three states that cost money are kept apart on purpose. `running` is the only one that
// served anything; `starting` is the cold start (165-197 s measured on a GPU box) and
// `draining` is the several minutes Managed Instances keeps the EC2 instance after the task
// is gone (427-477 s measured, ADR 0071 decision 7). Both bill, neither answers a request.
func engineUptimeSample(state string, secs int) store.EngineHourCounters {
	c := store.EngineHourCounters{Samples: 1, ObservedSecs: secs}
	switch state {
	case "running":
		c.RunningSecs = secs
	case "starting":
		c.StartingSecs = secs
	case "draining":
		c.DrainingSecs = secs
	}
	return c
}

// engineIdleWindow is the idle window the controller ACTUALLY applies: never shorter than the
// start deadline, or a window shorter than a cold start stops the service while it is still
// starting and the next request starts it again.
//
// It exists as a function so decideEngineAction and the panel's countdown cannot drift apart.
// They did not share it while the countdown did not exist, and a panel that counts down to a
// moment the controller does not act on is worse than one that shows nothing.
func engineIdleWindow(cfg engineControlCfg) time.Duration {
	if cfg.idle > 0 && cfg.idle < cfg.deadline {
		return cfg.deadline
	}
	return cfg.idle
}

// engineStopETA is when the controller will stop this engine by itself, or the zero time when
// that question has no answer. It refuses in four cases, and each refusal is the point:
//
//   - the mode is `on` or `off`. Under `on` the controller never stops it — a countdown there
//     is a promise of a saving that will not happen — and under `off` it is already stopping
//     or stopped for a reason that has nothing to do with idleness.
//   - the engine is not up. There is nothing to stop, and "would stop at" for a box that does
//     not exist reads as a schedule.
//   - the idle window is 0, which is configured to mean "never stop".
//   - nothing has ever stamped the demand mark. decideEngineAction judges nothing on that
//     pass either (engineReasonFirstPass), so there is genuinely no answer yet.
//
// The mark it counts from is the PERSISTED one (engineDemand.lastAt merges the stored value),
// so a CP that restarted mid-window still counts down to the right moment rather than
// restarting the clock.
func engineStopETA(mode string, up bool, lastDemand time.Time, cfg engineControlCfg) time.Time {
	if mode != engineModeOnDemand || !up || lastDemand.IsZero() {
		return time.Time{}
	}
	idle := engineIdleWindow(cfg)
	if idle <= 0 {
		return time.Time{}
	}
	return lastDemand.Add(idle)
}

// engineHourPoint is one hour of one engine. Zeroes are omitted by omitempty: a stopped hour
// carries nothing but its samples, and a 14-day window is 336 buckets.
type engineHourPoint struct {
	Hour string `json:"hour"` // YYYY-MM-DDTHH (UTC); the client shifts to local time
	store.EngineHourCounters
}

// engineHourlyResponse is what GET /api/admin/engines/{key}/hourly returns.
//
// ⚠️ There is no `observed` list beside `hours`, and that is not an omission. In
// usageHourlyResponse the heartbeat is a SEPARATE series because the workspace sweep walks
// every tenant and can come back having seen only half of them; here the controller watches one
// engine and cannot half-observe it, so the presence of an hour in `hours` IS the observation.
// The client must read a MISSING hour as unknown and leave the cell blank — never as stopped.
//
// IntervalSecs is the sampling resolution, for the footnote that tells a reader how coarse a
// cell is. It is NOT the denominator: ObservedSecs is, because the controller's interval drops
// to 5 seconds while an engine is starting and samples x interval would then exceed the hour.
type engineHourlyResponse struct {
	Engine       string            `json:"engine"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	IntervalSecs int               `json:"interval_secs"`
	Hours        []engineHourPoint `json:"hours"`
}

// buildEngineHourly folds store rows into the response. Pure, so a test can aim at it without
// HTTP or a sampler — the same split as buildUsageHourly.
func buildEngineHourly(key string, rows []store.EngineHourRow, from, to string, cfg engineControlCfg) engineHourlyResponse {
	out := engineHourlyResponse{
		Engine: key, From: from, To: to,
		IntervalSecs: int(cfg.interval.Seconds()),
		Hours:        []engineHourPoint{},
	}
	for _, r := range rows {
		out.Hours = append(out.Hours, engineHourPoint{Hour: r.Hour, EngineHourCounters: r.EngineHourCounters})
	}
	return out
}

// recordUptime writes one tick into engine_hourly.
//
// ⚠️ It claims at most one interval, and on the first tick of a process it claims NOTHING.
// A CP that was down for an hour comes back with a large elapsed time and a perfectly valid
// current state, and attributing that gap to the state it happens to see now would fill the
// outage with confident colour — the exact failure the three-state cell exists to prevent.
// The cost of the rule is one lost tick per CP start, which is 30 seconds an hour never has.
//
// The bucket is the hour the tick lands in, so a sample that spans an hour boundary is
// attributed wholly to the later hour. At a 30-second interval that is at most 30 seconds
// misplaced per boundary, the same approximation usage.go makes.
func (c *engineController) recordUptime(ctx context.Context, now time.Time, state string) {
	if c.uptime == nil || c.eng == nil || c.eng.key == "" {
		return
	}
	c.mu.Lock()
	prev := c.lastSample
	c.lastSample = now
	c.mu.Unlock()
	if prev.IsZero() {
		return // nothing to vouch for yet
	}
	secs := int(c.cfg.interval.Seconds())
	if elapsed := int(now.Sub(prev).Seconds()); elapsed < secs {
		secs = elapsed
	}
	if secs <= 0 {
		return
	}
	// Detached from the request context for the same reason the demand mark is: a tick that
	// is cancelled mid-write still observed what it observed.
	if err := c.uptime.AddEngineHour(context.WithoutCancel(ctx), c.eng.key,
		now.UTC().Format(usageHourFmt), engineUptimeSample(state, secs)); err != nil {
		log.Printf("%s: recording the hourly occupancy failed: %v", c.eng.logKey(), err)
	}
}
