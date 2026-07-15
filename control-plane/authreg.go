package main

import "sync"

// authRegistry tracks, per identity (userKey), the expiry of the last session
// cookie the CP observed for it. authGate records this on EVERY signature-valid
// request — expired or not — before it enforces expiry, so the map always reflects
// the newest cookie that identity presented.
//
// The idle reaper consults it to answer one question: is this workspace owner's
// login currently expired? If so, the reaper will NOT halt the owner's idle
// sessions or stop their workspace. Rationale: an expired login locks the user out
// — they cannot re-attach a terminal to keep a session "watched" — so reaping then
// would destroy live work they have no way to defend. Protection is unbounded and
// lifts automatically the moment they re-authenticate (a fresh valid cookie records
// a future expiry, so loginExpired flips back to false). The deliberate idle-stop
// still applies fully to authenticated-but-idle users; only a locked-out owner is
// spared.
//
// In-memory (like connRegistry): it resets on a CP restart, after which a workspace
// is simply unprotected until its owner's next request re-records the expiry. That
// is safe — the reaper's normal idle grace still applies in the meantime.
type authRegistry struct {
	mu  sync.Mutex
	exp map[string]int64 // userKey -> observed cookie expiry (unix seconds)
}

func newAuthRegistry() *authRegistry {
	return &authRegistry{exp: map[string]int64{}}
}

// observe records the expiry of the cookie this identity just presented. Called
// from authGate whenever the cookie signature verifies (whether or not it is past
// its expiry), so the newest expiry — including the one that just lapsed — is kept.
func (a *authRegistry) observe(userKey string, exp int64) {
	if a == nil || userKey == "" {
		return
	}
	a.mu.Lock()
	a.exp[userKey] = exp
	a.mu.Unlock()
}

// loginExpired reports whether we have observed this identity's cookie AND it is now
// past its expiry. An unknown identity (never observed, e.g. right after a CP
// restart) is NOT treated as expired — the reaper then falls back to normal
// idle-stop rather than protecting a workspace it knows nothing about.
func (a *authRegistry) loginExpired(userKey string, now int64) bool {
	if a == nil || userKey == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.exp[userKey]
	return ok && now > exp
}
