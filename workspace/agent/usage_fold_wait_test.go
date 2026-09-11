package main

import (
	"testing"
	"time"
)

// The bulk-fold wait's timeout, checked without spending it.
//
// 🔴 A deadline built on `time.Now()` can only be tested by waiting for it, so in practice it is
// never tested -- and this one quietly stopped measuring the code and started measuring the host:
// at 60 seconds, three suites running at once on this shared machine pushed an ordinary fold past
// the budget and failed whichever test was holding the wait (PRs #494 and #498; the same suite
// alone was green). The fix is not a bigger number on its own. It is that the number is now a
// parameter with the clock beside it, so what the loop decides is provable in microseconds.
//
// `fakeClock` only moves when the loop SLEEPS. That is what makes the last case meaningful: it
// proves the budget was consumed by the loop's own waiting and not by the test sitting there.
type fakeClock struct {
	now    time.Time
	slept  time.Duration
	sleeps int
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(d time.Duration) {
	c.now = c.now.Add(d)
	c.slept += d
	c.sleeps++
}

func TestWaitForIdleSpendsItsBudgetOnTheInjectedClock(t *testing.T) {
	t.Run("already idle: returns without sleeping at all", func(t *testing.T) {
		c := &fakeClock{now: time.Unix(0, 0)}
		if !waitForIdle(func() bool { return false }, time.Minute, c.Now, c.Sleep) {
			t.Fatal("an idle writer was reported as a timeout")
		}
		if c.sleeps != 0 {
			t.Fatalf("slept %d times while nothing was running", c.sleeps)
		}
	})

	t.Run("becomes idle: returns then, not at the budget", func(t *testing.T) {
		c := &fakeClock{now: time.Unix(0, 0)}
		left := 5
		busy := func() bool {
			if left == 0 {
				return false
			}
			left--
			return true
		}
		if !waitForIdle(busy, time.Minute, c.Now, c.Sleep) {
			t.Fatal("a writer that finished was reported as a timeout")
		}
		// Five polls returned busy, so exactly five sleeps -- and the budget is untouched.
		if c.sleeps != 5 {
			t.Fatalf("sleeps = %d, want 5 (one per busy poll)", c.sleeps)
		}
		if c.slept >= time.Minute {
			t.Fatalf("slept %s of a 1m budget for a writer that finished after 5 polls", c.slept)
		}
	})

	t.Run("never idle: gives up after the budget, on the fake clock", func(t *testing.T) {
		c := &fakeClock{now: time.Unix(0, 0)}
		start := time.Now()
		if waitForIdle(func() bool { return true }, time.Minute, c.Now, c.Sleep) {
			t.Fatal("a writer that never finished was reported as idle")
		}
		if c.slept < time.Minute {
			t.Fatalf("gave up after %s of a 1m budget", c.slept)
		}
		// The point of the whole exercise: a minute of budget, and no real time.
		if real := time.Since(start); real > 2*time.Second {
			t.Fatalf("the fake clock did not carry the wait: %s of real time elapsed", real)
		}
	})

	t.Run("the budget is read from the argument, not from a constant", func(t *testing.T) {
		// Without this, a wait hard-coded to 60s would still pass every case above.
		c := &fakeClock{now: time.Unix(0, 0)}
		waitForIdle(func() bool { return true }, 250*time.Millisecond, c.Now, c.Sleep)
		if c.slept < 250*time.Millisecond || c.slept > time.Second {
			t.Fatalf("slept %s for a 250ms budget", c.slept)
		}
	})
}
