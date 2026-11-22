package engine

import (
	"strings"
	"testing"
	"time"
)

func TestSessionTokenRoundTripAndExpiry(t *testing.T) {
	key := SignKey([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Unix(1_800_000_000, 0)
	tok := MintSessionToken(key, "m1", "s1", "k1", now.Add(time.Hour))
	c, ok := VerifySessionToken(key, tok, now)
	if !ok || c.MembershipID != "m1" || c.Session != "s1" || c.Key != "k1" {
		t.Fatalf("round trip: %+v %v", c, ok)
	}
	if mid, ok := SessionMembership(tok); !ok || mid != "m1" {
		t.Fatalf("membership: %q %v", mid, ok)
	}
	if _, ok := VerifySessionToken(key, tok, now.Add(time.Hour)); ok {
		t.Fatal("expired token accepted")
	}
	if _, ok := VerifySessionToken(SignKey([]byte("other-master-other-master-other!!")), tok, now); ok {
		t.Fatal("token verified under a different key")
	}
}

func TestIssueAndSessionTokensAreNotInterchangeable(t *testing.T) {
	key := SignKey([]byte("0123456789abcdef0123456789abcdef"))
	issue := MintIssueToken(key, "m1")
	if mid, ok := VerifyIssueToken(key, issue); !ok || mid != "m1" {
		t.Fatalf("issue round trip: %q %v", mid, ok)
	}
	if _, ok := VerifySessionToken(key, strings.Replace(issue, "afei_", "afe_", 1), time.Unix(1, 0)); ok {
		t.Fatal("issuing token replayed as a session token")
	}
	if _, ok := VerifyIssueToken(key, issue[:len(issue)-2]+"xx"); ok {
		t.Fatal("tampered tag accepted")
	}
}

func TestTagBindsTokenKind(t *testing.T) {
	key := SignKey([]byte("0123456789abcdef0123456789abcdef"))
	if tag(key, "issue", "x") == tag(key, "session", "x") {
		t.Fatal("tag does not depend on the token kind")
	}
}

// Vectors produced by the pre-move implementation (engine_token.go in package main). Tokens
// already injected into running Workspaces must keep verifying, and minting must keep emitting
// the same bytes; a round trip alone cannot catch a symmetric change to the tag input.
func TestTokenWireCompatibility(t *testing.T) {
	key := SignKey([]byte("0123456789abcdef0123456789abcdef"))
	const issue = "afei_bTE.ScSTU7sgWF_6zatkiS5UCg"
	const session = "afe_bTEAczEAazEAMTgwMDAwMzYwMA.iQb6mqSEDprMDxhDnVx7og"
	exp := time.Unix(1_800_003_600, 0)
	if got := MintIssueToken(key, "m1"); got != issue {
		t.Fatalf("issue token = %q", got)
	}
	if got := MintSessionToken(key, "m1", "s1", "k1", exp); got != session {
		t.Fatalf("session token = %q", got)
	}
	if mid, ok := VerifyIssueToken(key, issue); !ok || mid != "m1" {
		t.Fatalf("legacy issue token refused: %q %v", mid, ok)
	}
	c, ok := VerifySessionToken(key, session, exp.Add(-time.Second))
	if !ok || c.MembershipID != "m1" || c.Session != "s1" || c.Key != "k1" || !c.Expires.Equal(exp) {
		t.Fatalf("legacy session token: %+v %v", c, ok)
	}
}
