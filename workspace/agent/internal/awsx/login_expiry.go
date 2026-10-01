package awsx

import (
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

// Warning before a cached IAM Identity Center login ends (#1029, ADR 0102 note of
// 2026-10-02). The Agent decides "expiring" on its own clock, so a browser whose clock is
// off cannot move the warning; the Console only shows what the Agent lists.

const (
	// ssoExpiryWarnBefore is how long before the end a login counts as expiring. The check
	// runs with the five-minute Settings poll, so the notification lands 10 to 15 minutes
	// before the end.
	ssoExpiryWarnBefore = 15 * time.Minute
	// NoticeKindAWSExpiring is the notification kind that names an expiring profile.
	NoticeKindAWSExpiring = "aws-sso-expiring"
)

// parseCacheTime reads a time botocore wrote into the SSO cache: RFC 3339 with "Z" today,
// "2006-01-02T15:04:05UTC" from older CLI releases. Both are UTC.
func parseCacheTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02T15:04:05UTC", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// readSSOExpiry returns the latest moment the cached login of ssoSession can still be
// used, as far as the cache says. Without a usable refresh token that is the access
// token's expiresAt. With one, the CLI renews the access token on use until the client
// registration expires (registrationExpiresAt) or the portal session ends; the cache does
// not record the portal session's end, so this is an upper bound, and a session the
// portal ends earlier is caught by af-aws-exec's login request instead. Neither token
// leaves this function; a missing or unreadable cache is "no expiry", never an error.
func readSSOExpiry(ssoSession string) (time.Time, bool) {
	var doc struct {
		AccessToken           string `json:"accessToken"`
		ExpiresAt             string `json:"expiresAt"`
		RefreshToken          string `json:"refreshToken"`
		ClientID              string `json:"clientId"`
		ClientSecret          string `json:"clientSecret"`
		RegistrationExpiresAt string `json:"registrationExpiresAt"`
	}
	if !readJSON(ssoCachePath(ssoSession), &doc) || doc.AccessToken == "" {
		return time.Time{}, false
	}
	end, ok := parseCacheTime(doc.ExpiresAt)
	if !ok {
		return time.Time{}, false
	}
	// botocore refreshes only with all three of these; without them the access token's
	// end is the end.
	if doc.RefreshToken != "" && doc.ClientID != "" && doc.ClientSecret != "" {
		if reg, ok := parseCacheTime(doc.RegistrationExpiresAt); ok && reg.After(end) {
			end = reg
		}
	}
	return end, true
}

// expiringAt reports whether end lies ahead of now by no more than ssoExpiryWarnBefore.
// A login that has already ended is not "expiring": af-aws-exec's request takes over.
func expiringAt(end, now time.Time) bool {
	return end.After(now) && end.Sub(now) <= ssoExpiryWarnBefore
}

// warnExpiringSSO files one notification per Settings profile and end whose login is
// about to end. The key holds the end, so a re-login (a new end) can warn again and a
// poll that sees the same end cannot. The payload carries the profile name only, and
// the Console shows it only if GET /aws-login/profiles lists that profile as expiring.
func warnExpiringSSO(now time.Time) {
	settings := loginSettings()
	names := make([]string, 0, len(settings))
	for name, sp := range settings {
		if IncompleteReason(sp) == "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		end, ok := readSSOExpiry("af-" + name)
		if !ok || !expiringAt(end, now) {
			continue
		}
		ev := notice.New(NoticeKindAWSExpiring, "", "", "")
		ev.TargetType = "workspace"
		ev.Payload["profile"] = name
		_ = notice.PutOnce("aws-sso-expiring:"+name+":"+end.Format(time.RFC3339), ev)
	}
}
