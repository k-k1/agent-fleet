package muse

// Usage-limit evidence for the shared auto-resume watch (sessionx/rate_limit_resume.go).
//
// The host has no typed signal for "this turn died on the subscription quota": measured on
// 2026-10-06 (muse-spark-1.3), turn/completed arrives as terminal=failed with
//
//	error: {kind: "modelError", retryable: false, message: "API error 429 [request_id=…]:
//	        Subscription quota exhausted. Your usage window resets at 2026-10-06T04:25:21Z.
//	        (rate_limit_error)"}
//
// so the message is the only evidence there is. The match is kept narrow (the provider's
// rate_limit_error tag AND either the quota wording or a reset instant) because a false
// positive books a wake-up that resends into a session that was never stuck.

import (
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// usageLimit is a turn failure that waiting clears.
type usageLimit struct {
	resetAt time.Time // zero when the message named no instant
}

var resetAtRe = regexp.MustCompile(`resets at (\d{4}-\d\d-\d\dT[0-9:.]+(?:Z|[+-]\d\d:\d\d))`)

// usageLimitOf classifies a failed turn's error. ok=false for every other failure.
func usageLimitOf(e *msp.TurnError) (usageLimit, bool) {
	if e == nil || !strings.Contains(e.Message, "rate_limit_error") {
		return usageLimit{}, false
	}
	var lim usageLimit
	if sm := resetAtRe.FindStringSubmatch(e.Message); sm != nil {
		if t, err := time.Parse(time.RFC3339, sm[1]); err == nil {
			lim.resetAt = t
		}
	}
	if lim.resetAt.IsZero() && !strings.Contains(strings.ToLower(e.Message), "quota") {
		return usageLimit{}, false
	}
	return lim, true
}

// IsRateLimited reports whether the session's last turn failed on its usage limit. The list
// badge (WireLive), the mirror/chat chip (sessionx.DriveState) and the auto-resume watch all
// ask this one question, so the row and the booking never disagree. The mark is cleared when
// the next turn starts, so it never sticks.
func IsRateLimited(name string) bool {
	_, ok := limitOf(name)
	return ok
}

// ResetAt is the instant the limit lifts, plus the evidence for it.
//
// The instant the error itself names comes first: it is what THIS turn was told. The quota
// observation (usage.go) is the fallback only while it still reads the window or the week as
// spent — a reading under 100% describes a window this turn did not run into, and waking on
// it just hits the same 429 again.
func ResetAt(name string, now time.Time) (time.Time, string, bool) {
	lim, ok := limitOf(name)
	if !ok {
		return time.Time{}, "", false
	}
	if !lim.resetAt.IsZero() {
		return lim.resetAt, "error", true
	}
	if q := lastQuota(); q != nil {
		var at time.Time
		if q.Window.UsedPercent >= 100 && q.Window.ResetsAtMs > 0 {
			at = time.UnixMilli(q.Window.ResetsAtMs)
		}
		if q.Weekly.UsedPercent >= 100 && q.Weekly.ResetsAtMs > 0 {
			if w := time.UnixMilli(q.Weekly.ResetsAtMs); w.After(at) {
				at = w
			}
		}
		if !at.IsZero() && at.After(now) {
			return at, "quota", true
		}
	}
	return time.Time{}, "", false
}

func limitOf(name string) (usageLimit, bool) {
	h := handleFor(name)
	if h == nil {
		return usageLimit{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.limit == nil {
		return usageLimit{}, false
	}
	return *h.limit, true
}
