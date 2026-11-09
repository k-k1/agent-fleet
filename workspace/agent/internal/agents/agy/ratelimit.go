package agy

import (
	"regexp"
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
	resetsInRe     = regexp.MustCompile(`(?i)\bresets in ((?:\d+h)?(?:\d+m)?(?:\d+s)?)\b`)
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
	if m := resetsInRe.FindStringSubmatch(msg); m != nil && strings.TrimSpace(m[1]) != "" {
		if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
			return now.Add(d), true
		}
	}
	return time.Time{}, true
}
