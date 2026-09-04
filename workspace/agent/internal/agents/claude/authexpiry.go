package claude

// Spot expired credentials BEFORE a turn dies of them (docs/log/47 §4-8).
//
// The detection in §4-7 is after the fact: only once a turn has died with 401 and a synthetic
// record is left in the transcript is it clear the login had expired. Neither before nor after
// does any state surface.
//
// The CLI's own warning cannot be relied on either. Reading the 2.1.231 binary, the startup
// hint (id="oauth-expiry-warning" — the "Your login expires in 1 day · run /login to renew"
// line) is decided like this:
//
//	if (apiProvider !== "firstParty" || !oauthLogin) return null
//	if (typeof refreshTokenExpiresAt !== "number") return null
//	if (expiresAt > refreshTokenExpiresAt + 3d) return null
//	const left = refreshTokenExpiresAt - now
//	if (left > 3d || left <= 0) return null          // ← null once it has expired
//	return { daysLeft: ceil(left / 1d) }             // drawn only when daysLeft <= 1
//
// So once it has expired the TUI carries no trace of it at all. What is left is a session that
// looks like it is waiting for input while the prompt that was sent never moves off "pending",
// with no cause visible from the Console (measured; user report 2026-08-14).
//
// The material is the same two epochs (ms) claude itself looks at. The token itself is never
// read (its value goes into neither a log nor a DTO — only timestamps and booleans do):
//
//	claudeAiOauth.expiresAt             access token deadline (measured: about 8 hours)
//	claudeAiOauth.refreshTokenExpiresAt refresh token deadline (measured: about 30 days)
//
// The access token is extended by a refresh, but once the refresh has expired nothing can
// extend it. So "certainly dead" can only be said when BOTH are past; while only the refresh
// has expired the last access token still works. The Console badge and the send guard use that
// stricter side (both expired), while the settings card's advance warning uses the refresh
// deadline.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// warnWindow is the remaining time at which a login counts as "expiring soon". It matches the
// window in which claude is willing to talk about the deadline at all (the 3d in
// expiresAt > refresh + 3d above). The CLI only prints its one line at a day or less, and as a
// startup hint that vanishes after 15 seconds, so the Console shows it quietly a little
// earlier — the Console can put it somewhere that does not disappear, and that difference is
// the room the user gets to notice.
const warnWindow = 3 * 24 * time.Hour

// Expiry is what the credentials file says about how long this login has left.
// Known=false means "nothing to judge on", NOT expired: not connected, running on an API key,
// or the file format changed. Claiming expiry with no material is a false positive that stops
// a working session.
type Expiry struct {
	Known   bool
	Access  time.Time // claudeAiOauth.expiresAt
	Refresh time.Time // claudeAiOauth.refreshTokenExpiresAt (the deadline to sign in again)
}

// Dead reports that no valid token remains: both the refresh and the access deadline are past.
// Only then is a session allowed to call itself expired and refuse to send free text.
func (e Expiry) Dead(now time.Time) bool {
	if !e.Known {
		return false
	}
	end := e.Refresh
	if e.Access.After(end) {
		end = e.Access
	}
	return !now.Before(end)
}

// Soon reports that the login is inside warnWindow of its renewal deadline (but not
// dead yet) — for the settings card's "expires in N days".
func (e Expiry) Soon(now time.Time) bool {
	if !e.Known || e.Dead(now) {
		return false
	}
	return e.Refresh.Sub(now) <= warnWindow
}

// DaysLeft is the whole days left until the renewal deadline, rounded up the way
// claude rounds its own banner (ceil). 0 = expires today, or has expired already.
func (e Expiry) DaysLeft(now time.Time) int {
	if !e.Known {
		return 0
	}
	left := e.Refresh.Sub(now)
	if left <= 0 {
		return 0
	}
	return int((left + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
}

// credsFile is the shape we read out of claude's .credentials.json. AccessToken is
// here only because oauthToken() (usage.go) needs the same file — nothing else in
// this package may touch it, and it must never leave the process.
type credsFile struct {
	ClaudeAiOauth struct {
		AccessToken           string `json:"accessToken"`
		ExpiresAt             int64  `json:"expiresAt"`             // ms epoch
		RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"` // ms epoch
	} `json:"claudeAiOauth"`
}

// expiryOf is the pure decision over one parsed credentials file — the part the tests
// pin. envToken says whether a token in the environment is what this is running on
// (CredentialExpiry below passes in the real environment).
//
// Cases it declines to judge (Known=false):
//   - running on an environment token (CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY): this file
//     is not used, so a stale copy of it means nothing.
//   - refreshTokenExpiresAt absent (0): claude itself declines on the same condition.
//   - access later than refresh + 3d: not the shape of an ordinary subscription OAuth record
//     (the CLI's null condition, unchanged). Never call it expired on a guess.
func expiryOf(c credsFile, envToken bool) Expiry {
	o := c.ClaudeAiOauth
	if envToken || o.RefreshTokenExpiresAt <= 0 {
		return Expiry{}
	}
	e := Expiry{
		Known:   true,
		Access:  time.UnixMilli(o.ExpiresAt),
		Refresh: time.UnixMilli(o.RefreshTokenExpiresAt),
	}
	if o.ExpiresAt > 0 && e.Access.After(e.Refresh.Add(warnWindow)) {
		return Expiry{}
	}
	return e
}

func credsPath() string { return filepath.Join(ConfigDir(), ".credentials.json") }

// envToken reports whether a token in the environment overrides the credentials file.
// claude reads CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY ahead of its own OAuth
// record, and sessions inherit the agent's environment, so a stale file must not be
// allowed to declare those sessions dead.
func envToken() bool {
	return os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || os.Getenv("ANTHROPIC_API_KEY") != ""
}

// The credentials are read once per session per session-list poll, so the parsed contents are
// reused until the stat (size + mtime) changes. Keyed on the stat rather than a TTL because a
// stale verdict lingering for tens of seconds right after re-authenticating reads as "I fixed
// it and it still says expired".
var (
	credMu    sync.Mutex
	credKey   string // size|modtime
	credCache credsFile
	credOK    bool
)

// readCreds returns the parsed credentials file, cached on its stat. ok=false when the
// file is absent or unparsable.
func readCreds() (credsFile, bool) {
	p := credsPath()
	key := ""
	if st, err := os.Stat(p); err == nil {
		// The path is part of the key: CLAUDE_CONFIG_DIR can be swapped (test isolation, a
		// future switch), and on mtime+size alone a different file that happens to match would
		// keep the previous verdict.
		key = p + "|" + st.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(st.Size(), 10)
	}
	credMu.Lock()
	defer credMu.Unlock()
	if key != "" && key == credKey {
		return credCache, credOK
	}
	credKey, credCache, credOK = key, credsFile{}, false
	b, err := os.ReadFile(p)
	if err != nil {
		return credCache, credOK
	}
	var c credsFile
	if json.Unmarshal(b, &c) != nil {
		return credCache, credOK
	}
	credCache, credOK = c, true
	return credCache, credOK
}

// CredentialExpiry reports what the local OAuth record says about this login's
// remaining life. Known=false when there is nothing to judge (see expiryOf).
func CredentialExpiry() Expiry {
	c, ok := readCreds()
	if !ok {
		return Expiry{}
	}
	return expiryOf(c, envToken())
}

// AuthExpired is the one-call form used by the live-state code and the send guard:
// this workspace's claude login can no longer run a turn.
//
// This is a workspace-wide fact (one credentials file per container), so every claude session
// reports it at once. It is not per-session state, but the session row is what the user looks
// at, so not showing it there means they never notice.
func AuthExpired() bool { return CredentialExpiry().Dead(time.Now()) }

// resetCredCache drops the stat cache. Right after re-authenticating or disconnecting the
// contents can change while the stat does not (rewritten in the same second at the same size),
// so those two paths discard it explicitly.
func resetCredCache() {
	credMu.Lock()
	credKey, credCache, credOK = "", credsFile{}, false
	credMu.Unlock()
}
