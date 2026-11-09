package agy

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// agy's quota refusal, as a headless `agy -p` prints it on stderr (exit 3, agy 1.5.3):
//
//	error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 108h50m38s.
//	AGY_ERROR: {"short_error":"RESOURCE_EXHAUSTED (code 429): Individual quota reached. …","status":"RESOURCE_EXHAUSTED",…}
//
// Read generically, the only hit is "code 429", which routehealth takes for a request-rate
// limit and parks for one minute. That is shorter than its NearRecovery, so no resolver ever
// passed agy over and every mirror translation failed for days until the window reset.
var (
	quotaReachedRe = regexp.MustCompile(`(?i)\bquota reached\b`)
	// "108h50m38s" is the measured form; days, spaces and capitals are accepted too, because a
	// reset read as zero turns a multi-day window into a backoff guess.
	// The unit is read as a whole word, so "5 months" or "5 ms" is never five minutes.
	resetsInRe  = regexp.MustCompile(`(?i)\bresets in ((?:\d+\s*[a-z]+\s*)+)`)
	resetPartRe = regexp.MustCompile(`(?i)(\d+)\s*([a-z]+)`)
)

// LimitInMessage reports whether msg is agy's usage-quota refusal and, when the text names
// one, when it lifts ("Resets in 108h50m38s", relative to now). resetAt is zero when no
// duration could be read.
//
// The quota is per model group, not per account: on the day this was measured the default
// model was refused while one-shots naming gpt-oss kept succeeding, so callers park the model,
// not the whole kind.
func LimitInMessage(msg string, now time.Time) (resetAt time.Time, ok bool) {
	if !quotaReachedRe.MatchString(msg) {
		return time.Time{}, false
	}
	if m := resetsInRe.FindStringSubmatch(msg); m != nil {
		if d := resetDuration(m[1]); d > 0 {
			return now.Add(d), true
		}
	}
	return time.Time{}, true
}

var resetUnits = map[string]time.Duration{
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"s": time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
}

// resetDuration reads "108h50m38s" / "1d 2h" / "2 days". Any unit it does not know makes the
// whole reading zero: a wrong reset is worse than the backoff guess a zero falls back to.
func resetDuration(s string) time.Duration {
	var d time.Duration
	for _, p := range resetPartRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(p[1])
		u, ok := resetUnits[strings.ToLower(p[2])]
		if err != nil || !ok {
			return 0
		}
		d += time.Duration(n) * u
	}
	return d
}
