//go:build contract || contract_live || contract_manual

package testguard

import (
	"os"
	"testing"
)

// A contract binary reads the real CLIs' sign-in from HOME; with the scratch HOME every
// contract fails "not signed in" before testing anything. Deliberately independent of
// keepCredentials, so flipping that constant turns this red.
func TestContractBinaryKeepsRealHome(t *testing.T) {
	if got := os.Getenv("HOME"); got != realHome || realHome == "" {
		t.Fatalf("HOME = %q, want the real %q", got, realHome)
	}
}
