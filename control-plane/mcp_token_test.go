// mcp_token_test.go — AF_MCP_TOKEN round-trip. MintMCPToken / VerifyMCPToken live in
// internal/mcpsrv, but this test has to stay in package main.
//
// The reason is the last entry of the bad list: "reject another bridge's credential" is
// built from the schedule bridge's real mintScheduleToken, which mcpsrv cannot call.
// Moved over there the case degrades into a string literal and stops detecting the real
// failure — schedule colliding with MCP on prefix and signing domain. The memo bridge
// already uses the same `afm_` prefix; the signing-domain string is all that separates
// the two.

package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
)

func TestMCPTokenRoundTrip(t *testing.T) {
	key := mcpsrv.MCPSignKey([]byte("0123456789abcdef0123456789abcdef"))
	tok := mcpsrv.MintMCPToken(key, "membership-1")
	// Deterministic, so re-injection on every container start is idempotent.
	if tok != mcpsrv.MintMCPToken(key, "membership-1") {
		t.Fatal("the token must be deterministic per membership")
	}
	mid, ok := mcpsrv.VerifyMCPToken(key, tok)
	if !ok || mid != "membership-1" {
		t.Fatalf("verify: %q %v", mid, ok)
	}
	bad := []string{
		"", "nope", "afm_", "afm_bad.tag",
		mcpsrv.MintMCPToken(key, "membership-1") + "x",                                                 // tampered tag
		mintScheduleToken(scheduleSignKey([]byte("0123456789abcdef0123456789abcdef")), "membership-1"), // wrong credential
	}
	for _, b := range bad {
		if _, ok := mcpsrv.VerifyMCPToken(key, b); ok {
			t.Fatalf("must reject %q", b)
		}
	}
	// A different master key must not validate: the MCP token is its own credential, so a
	// memo/schedule token leak grants nothing here (and vice versa).
	if _, ok := mcpsrv.VerifyMCPToken(mcpsrv.MCPSignKey([]byte("ffffffffffffffffffffffffffffffff")), tok); ok {
		t.Fatal("a token from another deployment key must be rejected")
	}
}
