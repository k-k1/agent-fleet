package agents

// The "shut down when nothing needs it" watch for the shared daemons (codex app-server /
// opencode serve).
//
// Keeping either daemon resident is expensive (measured RSS: codex app-server about 110 MB =
// 62 MB native + 48 MB node shim, opencode serve about 305 MB), while a cold start costs codex
// only 217 ms. There is no case for keeping them up from boot, so they stay down while there is
// no demand (managed handles, and for codex the TUI sessions running with --remote).
//
// The supervisor arms exactly one watch when Ensure succeeds, and the watch ends itself once
// it stops the daemon (the next Ensure that needs one arms it again). The race between
// deciding and stopping is settled inside stopIfIdle, which re-checks needs under the lock:
// getting this wrong pulls a live daemon out from under its users, so the loop's own verdict
// is never trusted alone.

import (
	"log"
	"os"
	"strconv"
	"time"
)

// idleTick is how often demand is observed; it only has to be well finer than the grace
// period before a stop. It is a var so tests can shorten it.
var idleTick = 15 * time.Second

// IdleTickForTest swaps the observation interval and returns the previous value,
// so a test in another package can drive the loop without waiting real seconds.
func IdleTickForTest(d time.Duration) time.Duration {
	prev := idleTick
	idleTick = d
	return prev
}

// IdleGrace reads a "stop after <n> seconds at zero demand" knob from env. 0 disables the
// automatic stop (once up, the daemon stays until the Agent dies). An invalid value falls
// back to the default.
func IdleGrace(env string, def time.Duration) time.Duration {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// WatchIdle runs the observation loop: once needs() has been 0 for grace, it calls
// stopIfIdle(). A true result (stopped, or already stopped) ends the loop; false means the
// re-check under the lock found demand again, so the grace period restarts and the watch goes
// on. With grace <= 0 no watch is armed at all.
func WatchIdle(name string, needs func() int, stopIfIdle func() bool, grace time.Duration) {
	if grace <= 0 {
		return
	}
	var idleSince time.Time
	for {
		time.Sleep(idleTick)
		if needs() > 0 {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) < grace {
			continue
		}
		if stopIfIdle() {
			return
		}
		// The re-check under the lock found demand again (a session started just before the
		// shutdown). The supervisor records the outcome, so here we only restart the count.
		log.Printf("%s: skipped the stop after %s at zero demand (demand came back)", name, grace)
		idleSince = time.Time{}
	}
}
