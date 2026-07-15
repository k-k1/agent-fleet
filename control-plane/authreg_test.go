package main

import "testing"

// TestAuthRegistry pins the reaper's "is this owner locked out?" primitive: an
// observed-then-lapsed cookie reads expired; a valid or re-issued one does not; an
// identity we've never seen is never treated as expired (so the reaper falls back to
// normal idle-stop rather than protecting a workspace it knows nothing about).
func TestAuthRegistry(t *testing.T) {
	const now int64 = 1_000_000

	reg := newAuthRegistry()

	// Never observed -> not expired (unknown owner, e.g. right after a CP restart).
	if reg.loginExpired("ghost@x.com", now) {
		t.Fatal("unobserved identity must not read as expired")
	}

	// Observed with a future expiry (a valid login) -> not expired.
	reg.observe("alice@x.com", now+3600)
	if reg.loginExpired("alice@x.com", now) {
		t.Fatal("valid (future-exp) login must not read as expired")
	}

	// Observed with a past expiry (the lapsed cookie authGate recorded) -> expired.
	reg.observe("bob@x.com", now-1)
	if !reg.loginExpired("bob@x.com", now) {
		t.Fatal("lapsed login must read as expired")
	}

	// Re-auth: a fresh cookie with a future expiry lifts the protection immediately.
	reg.observe("bob@x.com", now+7200)
	if reg.loginExpired("bob@x.com", now) {
		t.Fatal("re-authenticated login must no longer read as expired")
	}

	// Exactly at exp is not yet past it (strict now > exp).
	reg.observe("edge@x.com", now)
	if reg.loginExpired("edge@x.com", now) {
		t.Fatal("now == exp must not read as expired (strict inequality)")
	}

	// Nil receiver / empty key are safe no-ops.
	var nilReg *authRegistry
	nilReg.observe("x@x.com", now) // must not panic
	if nilReg.loginExpired("x@x.com", now) {
		t.Fatal("nil registry must not read as expired")
	}
	if reg.loginExpired("", now) {
		t.Fatal("empty key must not read as expired")
	}
}
