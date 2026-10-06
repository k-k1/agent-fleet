package muse

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// measuredQuotaMessage is the turn/completed error text a muse-spark-1.3 host sent on
// 2026-10-06 when the subscription window ran out (request id shortened).
const measuredQuotaMessage = "API error 429 [request_id=8af4fc75]: Subscription quota exhausted. " +
	"Your usage window resets at 2026-10-06T04:25:21Z. (rate_limit_error)"

func TestUsageLimitOf(t *testing.T) {
	measuredReset := time.Date(2026, 10, 6, 4, 25, 21, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		err     *msp.TurnError
		want    bool
		resetAt time.Time
	}{
		{"measured", &msp.TurnError{Kind: "modelError", Message: measuredQuotaMessage}, true, measuredReset},
		{"quota without an instant", &msp.TurnError{Kind: "modelError",
			Message: "API error 429: Subscription quota exhausted. (rate_limit_error)"}, true, time.Time{}},
		// A per-minute throttle the host gave up retrying is not a window waiting clears.
		{"bare rate_limit_error", &msp.TurnError{Kind: "modelError",
			Message: "API error 429: Too many requests. (rate_limit_error)"}, false, time.Time{}},
		{"other model error", &msp.TurnError{Kind: "modelError", Message: "API error 500: overloaded"}, false, time.Time{}},
		{"quota wording without the tag", &msp.TurnError{Kind: "toolFailure", Message: "disk quota exceeded"}, false, time.Time{}},
		{"nil", nil, false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lim, ok := usageLimitOf(tc.err)
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !lim.resetAt.Equal(tc.resetAt) {
				t.Errorf("resetAt = %v, want %v", lim.resetAt, tc.resetAt)
			}
		})
	}
}

// A turn that failed on the quota marks the handle; the next turn starting clears it, so the
// "limited" badge and the auto-resume never outlive the limit.
func TestQuotaFailureMarksTheHandleUntilTheNextTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, h.name, h)

	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
		TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalFailed,
		Error: &msp.TurnError{Kind: "modelError", Message: measuredQuotaMessage},
	})
	waitEvent(t, h, agents.TurnFailed)
	if !IsRateLimited(h.name) {
		t.Fatal("IsRateLimited = false after a turn failed on the subscription quota")
	}
	at, source, ok := ResetAt(h.name, time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC))
	if !ok || source != "error" || !at.Equal(time.Date(2026, 10, 6, 4, 25, 21, 0, time.UTC)) {
		t.Errorf("ResetAt = %v %q %v, want the instant the error named", at, source, ok)
	}

	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-2", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	if IsRateLimited(h.name) {
		t.Error("IsRateLimited stayed true after the next turn started")
	}
}

func TestOtherFailuresDoNotMarkTheHandle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, h.name, h)
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{
		TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalFailed,
		Error: &msp.TurnError{Kind: "modelError", Message: "API error 500: overloaded"},
	})
	waitEvent(t, h, agents.TurnFailed)
	if IsRateLimited(h.name) {
		t.Error("IsRateLimited = true for a failure that is not a usage limit")
	}
}

// Without an instant in the error, the quota observation is used only while it still reads
// the window as spent: a reading under 100% describes a window this turn did not hit.
func TestResetAtFallsBackToASpentQuotaWindow(t *testing.T) {
	resetQuota(t)
	now := time.Now()
	h := &threadHandle{limit: &usageLimit{}}
	name := "test-" + t.Name()
	registerHandle(t, name, h)

	if _, _, ok := ResetAt(name, now); ok {
		t.Fatal("ResetAt ok with no instant in the error and no quota reading")
	}
	window := now.Add(90 * time.Minute)
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: now.UnixMilli(),
		Window: msp.SubscriptionUsageWindow{UsedPercent: 40, ResetsAtMs: window.UnixMilli(), WindowDurationMins: 300}})
	if _, _, ok := ResetAt(name, now); ok {
		t.Error("ResetAt ok from a window that is not spent")
	}
	recordQuota(msp.SubscriptionUsage{ObservedAtMs: now.UnixMilli() + 1,
		Window: msp.SubscriptionUsageWindow{UsedPercent: 100, ResetsAtMs: window.UnixMilli(), WindowDurationMins: 300}})
	at, source, ok := ResetAt(name, now)
	if !ok || source != "quota" || at.UnixMilli() != window.UnixMilli() {
		t.Errorf("ResetAt = %v %q %v, want the spent window's reset", at, source, ok)
	}
}

// The list badge: an idle muse whose last turn hit the quota reads "limited".
func TestWireLiveReportsLimited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "test-" + t.Name(), Dir: t.TempDir(), Kind: session.KindMuse}
	h := &threadHandle{}
	registerHandle(t, m.Name, h)
	if li := (agentImpl{}).WireLive(m, true); li.State == agents.StateLimited {
		t.Fatalf("state = %q before any limit", li.State)
	}
	h.mu.Lock()
	h.limit = &usageLimit{}
	h.mu.Unlock()
	if li := (agentImpl{}).WireLive(m, true); li.State != agents.StateLimited {
		t.Errorf("state = %q, want %q", li.State, agents.StateLimited)
	}
}
