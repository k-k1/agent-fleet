package routehealth

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func TestBackoffDoublesToTheCap(t *testing.T) {
	tb := NewTable()
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	now := t0
	for i, w := range want {
		tb.Fail("codex", "model-a", "429 Too Many Requests", now)
		until, class, ok := tb.Until("codex", "model-a", now)
		if !ok || class != ClassRateLimit || until.Sub(now) != w {
			t.Fatalf("strike %d: got (%v, %s, %v), want %v", i+1, until.Sub(now), class, ok, w)
		}
		now = until // the next strike lands as the park ends
	}
	if got := backoff(20); got != BackoffMax {
		t.Errorf("backoff(20) = %v, want %v", got, BackoffMax)
	}
}

func TestStaleStreakStartsOver(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "429", t0)
	tb.Fail("codex", "model-a", "429", t0.Add(time.Minute))
	later := t0.Add(24 * time.Hour)
	tb.Fail("codex", "model-a", "429", later)
	if until, _, _ := tb.Until("codex", "model-a", later); until.Sub(later) != BackoffMin {
		t.Errorf("a failure a day later parked for %v, want %v", until.Sub(later), BackoffMin)
	}
}

func TestModelScopeLeavesOtherModels(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "503 Service Unavailable", t0)
	if _, _, ok := tb.Until("codex", "model-b", t0); ok {
		t.Error("a 5xx on model-a parked model-b")
	}
	if _, _, ok := tb.Until("codex", "model-a", t0); !ok {
		t.Error("model-a not parked")
	}
}

func TestKindScopeParksEveryModel(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "You've hit your usage limit.", t0)
	if _, class, ok := tb.Until("codex", "model-b", t0); !ok || class != ClassWindow {
		t.Errorf("an account window did not park model-b: (%s, %v)", class, ok)
	}
}

func TestResetTimeWinsOverBackoff(t *testing.T) {
	tb := NewTable()
	reset := t0.Add(3 * time.Hour)
	tb.Park("muse", "", Verdict{Class: ClassWindow, Scope: ScopeKind, ResetAt: reset}, t0)
	if until, _, _ := tb.Until("muse", "any", t0); !until.Equal(reset) {
		t.Errorf("until = %v, want the named reset %v", until, reset)
	}
	// A reset beyond any real window is a misread and falls back to the backoff.
	tb2 := NewTable()
	tb2.Park("muse", "", Verdict{Class: ClassWindow, Scope: ScopeKind, ResetAt: t0.Add(30 * 24 * time.Hour)}, t0)
	if until, _, _ := tb2.Until("muse", "", t0); until.Sub(t0) != BackoffMin {
		t.Errorf("implausible reset parked for %v, want %v", until.Sub(t0), BackoffMin)
	}
}

func TestSpendParksForTheLongestStep(t *testing.T) {
	tb := NewTable()
	tb.Fail("claude", "", "You've hit your org's monthly spend limit · run /usage-credits to raise it", t0)
	if until, class, _ := tb.Until("claude", "model-a", t0); class != ClassSpend || until.Sub(t0) != BackoffMax {
		t.Errorf("spend cap: (%v, %s), want (%v, spend)", until.Sub(t0), class, BackoffMax)
	}
}

func TestConfigAndAuthDoNotPark(t *testing.T) {
	tb := NewTable()
	for _, msg := range []string{"404 model not found", "401 Unauthorized", "exit status 1"} {
		if v := tb.Fail("codex", "model-a", msg, t0); v.Cools() {
			t.Errorf("%q classified as %s, which cools", msg, v.Class)
		}
	}
	if _, _, ok := tb.Until("codex", "model-a", t0); ok {
		t.Error("a non-availability failure parked the route")
	}
}

func TestSucceedClearsModelAndKind(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "You've hit your usage limit.", t0)
	tb.Fail("codex", "model-a", "429", t0)
	tb.Succeed("codex", "model-a")
	if _, _, ok := tb.Until("codex", "model-a", t0); ok {
		t.Error("a success left the route parked")
	}
}

func TestAvoidIgnoresANearRecovery(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "429", t0) // parks for BackoffMin, within NearRecovery
	if tb.Avoid("codex", "model-a", t0) {
		t.Error("a park ending within NearRecovery caused a switch")
	}
	tb.Park("codex", "model-a", Verdict{Class: ClassServer, Scope: ScopeModel, ResetAt: t0.Add(time.Hour)}, t0)
	if !tb.Avoid("codex", "model-a", t0) {
		t.Error("an hour-long park did not cause a switch")
	}
	if tb.Avoid("codex", "model-a", t0.Add(2*time.Hour)) {
		t.Error("an expired park still avoided")
	}
}

// TestAvoidAGuessedWindowAtOnce: a usage window with no named reset parks for the short first
// backoff step, yet it lasts hours, so it must cause a switch at once.
func TestAvoidAGuessedWindowAtOnce(t *testing.T) {
	tb := NewTable()
	tb.Fail("codex", "model-a", "You've hit your usage limit.", t0)
	if !tb.Avoid("codex", "model-a", t0) {
		t.Error("a usage window without a named reset did not cause a switch")
	}
	// A named reset that is near is honoured: waiting beats switching.
	tb2 := NewTable()
	tb2.Park("muse", "", Verdict{Class: ClassWindow, Scope: ScopeKind, ResetAt: t0.Add(time.Minute)}, t0)
	if tb2.Avoid("muse", "", t0) {
		t.Error("a window named to reset within NearRecovery caused a switch")
	}
}
