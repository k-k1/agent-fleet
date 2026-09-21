package lcpp

// Reproduces the 2026-09-21 live finding: a warm engine's cold-start indicator once read
// "エンジン起動待ち（9223372036秒）" — math.MaxInt64 nanoseconds truncated to seconds — because
// setState only stamped runningSince on TurnRunning, leaving it at time.Time{}'s zero value for
// the entire TurnStarting window where the engine-wake wait actually happens (runTurn calls
// harness.EngineToken/harness.Run while still TurnStarting, only reaching TurnRunning once a
// connection exists). time.Since(zero value) does not panic; time.Time.Sub clamps to the maximum
// representable time.Duration (~292 years) on overflow, and Duration.Seconds() of that is exactly
// math.MaxInt64/1e9 truncated to an int — 9223372036.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// TestSetStateStampsRunningSinceOnTurnStarting pins the actual fix: entering TurnStarting must
// stamp runningSince immediately, not leave it for the later (and, during an engine wake,
// possibly never-reached-for-minutes) transition to TurnRunning.
func TestSetStateStampsRunningSinceOnTurnStarting(t *testing.T) {
	h := &threadHandle{events: make(chan agents.Event, 4)}
	before := time.Now()
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	since := h.runningSince
	h.mu.Unlock()
	if since.IsZero() {
		t.Fatal("setState(TurnStarting) left runningSince at its zero value — lastSay would report " +
			"an elapsed time of ~9223372036 seconds (time.Duration's overflow clamp) once past the threshold")
	}
	if since.Before(before) {
		t.Fatalf("runningSince = %v, want at/after %v", since, before)
	}
}

// TestLastSayZeroRunningSinceNeverShown is lastSay's own defence in depth: even if some future
// caller reaches TurnStarting/TurnRunning without going through setState (or a new TurnState is
// added to the "show the waking line" set without also being added to setState's stamp list),
// a zero-value runningSince must read as "nothing to show" rather than an overflowed duration.
func TestLastSayZeroRunningSinceNeverShown(t *testing.T) {
	h := &threadHandle{events: make(chan agents.Event, 4)}
	h.mu.Lock()
	h.state = agents.TurnStarting // bypasses setState — runningSince stays its zero value
	h.mu.Unlock()
	if s := h.lastSay(); s != "" {
		t.Fatalf("lastSay() = %q with a zero-value runningSince, want empty", s)
	}
}

// TestLastSayElapsedStartsAtZeroAndGrowsWithoutOverflow drives lastSay through setState the way
// runTurn actually does, then fast-forwards by backdating runningSince (real time.Sleep would
// make this test minutes long) to confirm the reported elapsed count is the real, small number —
// never the overflow clamp — and increases as time passes.
func TestLastSayElapsedStartsAtZeroAndGrowsWithoutOverflow(t *testing.T) {
	h := &threadHandle{events: make(chan agents.Event, 4)}
	h.setState(agents.TurnStarting)

	// Immediately after entering TurnStarting, elapsed is ~0s: below wakingLastSayThreshold, so
	// nothing is shown yet — "starts at zero".
	if s := h.lastSay(); s != "" {
		t.Fatalf("lastSay() = %q immediately after TurnStarting, want empty (below threshold)", s)
	}

	seconds := func(back time.Duration) int {
		h.mu.Lock()
		h.runningSince = time.Now().Add(-back)
		h.mu.Unlock()
		s := h.lastSay()
		if s == "" {
			t.Fatalf("lastSay() = \"\" with %s elapsed, want the waking line", back)
		}
		if strings.Contains(s, "9223372036") {
			t.Fatalf("lastSay() = %q reproduces the MaxInt64/1e9 overflow at %s elapsed", s, back)
		}
		digits := strings.TrimSuffix(strings.TrimPrefix(s, "エンジン起動待ち（"), "秒）")
		n, err := strconv.Atoi(digits)
		if err != nil {
			t.Fatalf("lastSay() = %q, could not parse elapsed seconds: %v", s, err)
		}
		return n
	}

	// "grows": a later backdate reports a larger elapsed count than an earlier one, and both
	// stay in a sane range — nowhere near a 292-year clamp.
	early := seconds(6 * time.Second)
	later := seconds(20 * time.Second)
	if early < 5 || early > 8 {
		t.Fatalf("elapsed at ~6s backdate = %d, want ~6", early)
	}
	if later < 19 || later > 22 {
		t.Fatalf("elapsed at ~20s backdate = %d, want ~20", later)
	}
	if later <= early {
		t.Fatalf("elapsed did not grow: early=%d later=%d", early, later)
	}
}

// TestLastSaySanityCeilingSuppressesAbsurdElapsed is the "add an upper-bound sanity check" half
// of the fix: even a runningSince stamped correctly but implausibly far in the past (a future
// bug, a clock jump) must not be shown as a multi-year wait.
func TestLastSaySanityCeilingSuppressesAbsurdElapsed(t *testing.T) {
	h := &threadHandle{events: make(chan agents.Event, 4)}
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	h.runningSince = time.Now().Add(-48 * time.Hour)
	h.mu.Unlock()
	if s := h.lastSay(); s != "" {
		t.Fatalf("lastSay() = %q with a 48h-old runningSince, want empty (past wakingLastSayCeiling)", s)
	}
}
